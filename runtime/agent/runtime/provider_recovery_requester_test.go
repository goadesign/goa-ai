package runtime

// These tests use the owner and requester together. They distinguish phase
// permission from acceptance, prove actual wait observations, and preserve Stop
// when an older permission or a late successful result reaches the requester.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
)

func TestProviderRecoveryRequesterWaitsForRootSettlement(t *testing.T) {
	now := time.Unix(100, 0)
	failure := recoveryOwnerFailure("first", now.Add(-time.Second), now)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: time.Minute})
	owned, _, err := owner.open(now, failure)
	require.NoError(t, err)
	requester := newProviderRecoveryRequester(failure)
	wait, err := requester.requestWait(5 * time.Second)
	require.NoError(t, err)
	_, err = requester.enter(now)
	require.ErrorContains(t, err, "without permission")
	decisions, err := owner.receive(now, owned, wait)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	_, err = requester.receive(now, decisions[0].message)
	require.NoError(t, err)
	_, err = requester.enter(now)
	require.NoError(t, err)
	unentered, err := requester.receive(now, engine.ProviderWaitObservationRequest{})
	require.NoError(t, err)
	assert.Nil(t, unentered, "permission does not prove that a timer was scheduled")
	entry, err := requester.waitEntered()
	require.NoError(t, err)
	_, err = owner.receive(now, owned, entry)
	require.NoError(t, err)
	observation, err := requester.receive(now.Add(2*time.Second), engine.ProviderWaitObservationRequest{})
	require.NoError(t, err)
	assert.Equal(t, engine.ProviderWaitObserved{StartedAt: now, Through: now.Add(2 * time.Second)}, observation)
	_, err = owner.receive(now.Add(2*time.Second), owned, observation)
	require.NoError(t, err)
	finished, err := requester.finishWait(now.Add(5 * time.Second))
	require.NoError(t, err)
	_, err = requester.requestAttempt(time.Time{})
	require.ErrorContains(t, err, "before wait settlement")
	decisions, err = owner.receive(now.Add(5*time.Second), owned, finished)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	_, err = requester.receive(now.Add(5*time.Second), decisions[0].message)
	require.NoError(t, err)
	attempt, err := requester.requestAttempt(time.Time{})
	require.NoError(t, err)
	decisions, err = owner.receive(now.Add(5*time.Second), owned, attempt)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	_, err = requester.receive(now.Add(5*time.Second), decisions[0].message)
	require.NoError(t, err)
	_, err = requester.enter(now.Add(5 * time.Second))
	require.NoError(t, err)
	success, err := requester.succeed()
	require.NoError(t, err)
	assert.NotEqual(t, providerRecoveryRequestComplete, requester.phase)
	decisions, err = owner.receive(now.Add(6*time.Second), owned, success)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	_, err = requester.receive(now.Add(6*time.Second), decisions[0].message)
	require.NoError(t, err)
	assert.Equal(t, providerRecoveryRequestComplete, requester.phase)
	assert.Equal(t, 54*time.Second, owner.budget.Remaining)
}

func TestProviderRecoveryRequesterRejectsEntryAtOriginalExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	requester := newProviderRecoveryRequester(recoveryOwnerFailure("first", now, now))
	_, err := requester.requestWait(time.Second)
	require.NoError(t, err)
	expiry := now.Add(time.Second)
	_, err = requester.receive(expiry, engine.ProviderRecoveryPermission{ExpiresAt: expiry})
	require.NoError(t, err)
	proof, err := requester.enter(expiry)
	require.ErrorContains(t, err, "expired before phase entry")
	assert.IsType(t, engine.ProviderPhaseUnused{}, proof)
	assert.False(t, requester.entered)
	assert.Equal(t, expiry, requester.expiry)
	observation, err := requester.receive(expiry, engine.ProviderWaitObservationRequest{})
	require.NoError(t, err)
	assert.Nil(t, observation)
}

func TestProviderRecoveryRequesterStopWinsOverDelayedPermission(t *testing.T) {
	now := time.Unix(100, 0)
	requester := newProviderRecoveryRequester(recoveryOwnerFailure("first", now, now))
	_, err := requester.requestWait(time.Second)
	require.NoError(t, err)
	requester.stop(context.Canceled)
	proof, err := requester.receive(now, engine.ProviderRecoveryPermission{ExpiresAt: now.Add(time.Second)})
	require.NoError(t, err)
	assert.IsType(t, engine.ProviderPhaseUnused{}, proof)
	_, err = requester.enter(now)
	require.ErrorIs(t, err, context.Canceled)
	_, err = requester.receive(now, engine.ProviderRecoverySettled{})
	require.NoError(t, err)
	_, err = requester.requestAttempt(time.Time{})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, requester.entered)
}

func TestProviderRecoveryRequesterCancellationReleasesOnlyUnenteredPermission(t *testing.T) {
	now := time.Unix(100, 0)
	failure := recoveryOwnerFailure("first", now, now)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: time.Minute})
	owned, _, err := owner.open(now, failure)
	require.NoError(t, err)
	requester := newProviderRecoveryRequester(failure)
	wait, err := requester.requestWait(time.Second)
	require.NoError(t, err)
	decisions, err := owner.receive(now, owned, wait)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	_, err = requester.receive(now, decisions[0].message)
	require.NoError(t, err)
	require.NotNil(t, owned.hold)
	for _, message := range requester.stop(context.Canceled) {
		_, err := owner.receive(now, owned, message)
		require.NoError(t, err)
	}
	assert.Nil(t, owned.hold)
	require.ErrorIs(t, owned.stopped, context.Canceled)
	assert.Equal(t, time.Minute, owner.budget.Remaining)
	assert.True(t, owner.canCheckpoint())
}
