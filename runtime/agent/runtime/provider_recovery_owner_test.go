package runtime

// These tests drive the owner's real phase transitions with workflow timestamps.
// They check shared permissions, late failures, terminal precedence, and facts
// that must remain unknown after a timeout. Adapter delivery is tested separately.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestProviderRecoveryOwnerSharesWaitAndAttemptCoverage(t *testing.T) {
	now := time.Unix(100, 0)
	wf, err := installProviderRecovery(
		&routeWorkflowContext{ctx: t.Context(), now: func() time.Time { return now }},
		&providerRecoveryBudget{Remaining: 30 * time.Second},
	)
	require.NoError(t, err)
	owner := wf.actor.owner
	first, decisions, err := owner.open(now, recoveryOwnerFailure("first", now.Add(-5*time.Second), now))
	require.NoError(t, err)
	assert.Empty(t, decisions)
	second, decisions, err := owner.open(now, recoveryOwnerFailure("second", now.Add(-3*time.Second), now))
	require.NoError(t, err)
	assert.Empty(t, decisions)
	assert.Equal(t, 25*time.Second, owner.budget.Remaining)

	for _, request := range []*providerRecoveryRequest{first, second} {
		decisions, err := owner.receive(now, request, engine.ProviderWaitRequest{Delay: 10 * time.Second})
		require.NoError(t, err)
		require.Len(t, decisions, 1)
		assert.Equal(t, engine.ProviderRecoveryPermission{ExpiresAt: now.Add(10 * time.Second)}, decisions[0].message)
		decisions, err = owner.receive(now, request, engine.ProviderWaitObserved{StartedAt: now, Through: now})
		require.NoError(t, err)
		assert.Empty(t, decisions)
	}
	afterWait := now.Add(10 * time.Second)
	for _, request := range []*providerRecoveryRequest{first, second} {
		decisions, err := owner.receive(afterWait, request, engine.ProviderWaitFinished{StartedAt: now, EndedAt: afterWait})
		require.NoError(t, err)
		require.Len(t, decisions, 1)
		assert.IsType(t, engine.ProviderRecoverySettled{}, decisions[0].message)
		decisions, err = owner.receive(afterWait, request, engine.ProviderAttemptRequest{})
		require.NoError(t, err)
		require.Len(t, decisions, 1)
		assert.Equal(t, engine.ProviderRecoveryPermission{ExpiresAt: now.Add(25 * time.Second)}, decisions[0].message)
	}
	assert.Equal(t, 15*time.Second, owner.budget.Remaining)
	require.NoError(t, wf.actor.observe(wf))
	assert.Zero(t, wf.actor.elapsed, "descendant spending alone must not pause the owner's clock")

	for _, request := range []*providerRecoveryRequest{first, second} {
		decisions, err := owner.receive(afterWait.Add(time.Second), request, engine.ProviderAttemptSucceeded{})
		require.NoError(t, err)
		require.Len(t, decisions, 1)
		assert.IsType(t, engine.ProviderRecoverySettled{}, decisions[0].message)
	}
	assert.Equal(t, 15*time.Second, owner.budget.Remaining)
	assert.True(t, owner.canCheckpoint())
}

func TestProviderRecoveryOwnerWaitsForHeldCapacityWithoutExhaustion(t *testing.T) {
	now := time.Unix(100, 0)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: 10 * time.Second})
	first, _, err := owner.open(now, recoveryOwnerFailure("first", now.Add(-10*time.Second), now.Add(-8*time.Second)))
	require.NoError(t, err)
	decisions, err := owner.receive(now, first, engine.ProviderWaitRequest{Delay: 10 * time.Second})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	originalExpiry := first.hold.interval.end
	assert.Equal(t, now.Add(8*time.Second), originalExpiry)

	second, decisions, err := owner.open(now, recoveryOwnerFailure("late", now.Add(-8*time.Second), now.Add(-5*time.Second)))
	require.NoError(t, err)
	assert.Empty(t, decisions)
	decisions, err = owner.receive(now, second, engine.ProviderWaitRequest{Delay: 10 * time.Second})
	require.NoError(t, err)
	assert.Empty(t, decisions)
	assert.NoError(t, owner.stopped)
	assert.NoError(t, second.stopped)
	assert.Equal(t, providerRecoveryPendingWait, second.phase)
	assert.Equal(t, 5*time.Second, owner.budget.Remaining)
	assert.Equal(t, originalExpiry, first.hold.interval.end)

	decisions, err = owner.receive(now.Add(time.Second), first, engine.ProviderPhaseUnused{})
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	assert.IsType(t, engine.ProviderRecoveryStopped{}, decisions[0].message)
	assert.Same(t, second, decisions[1].request)
	assert.Equal(t, engine.ProviderRecoveryPermission{ExpiresAt: now.Add(6 * time.Second)}, decisions[1].message)
	assert.Equal(t, 5*time.Second, owner.budget.Remaining)
}

func TestProviderRecoveryOwnerAdmitsInPhaseRequestOrder(t *testing.T) {
	now := time.Unix(100, 0)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: 10 * time.Second})
	holder, _, err := owner.open(now, recoveryOwnerFailure("holder", now, now))
	require.NoError(t, err)
	_, err = owner.receive(now, holder, engine.ProviderWaitRequest{Delay: 10 * time.Second})
	require.NoError(t, err)
	first, _, err := owner.open(now, recoveryOwnerFailure("first", now, now))
	require.NoError(t, err)
	second, _, err := owner.open(now, recoveryOwnerFailure("second", now, now))
	require.NoError(t, err)
	later := now.Add(11 * time.Second)
	for _, request := range []*providerRecoveryRequest{second, first} {
		decisions, err := owner.receive(later, request, engine.ProviderWaitRequest{Delay: time.Second})
		require.NoError(t, err)
		assert.Empty(t, decisions)
	}
	decisions, err := owner.receive(later, holder, engine.ProviderPhaseUnused{})
	require.NoError(t, err)
	require.Len(t, decisions, 3)
	assert.IsType(t, engine.ProviderRecoveryStopped{}, decisions[0].message)
	assert.Same(t, second, decisions[1].request)
	assert.Same(t, first, decisions[2].request)
}

func TestProviderRecoveryOwnerLateInitialOverrunStopsIssuedPermission(t *testing.T) {
	now := time.Unix(100, 0)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: 10 * time.Second})
	first, _, err := owner.open(now, recoveryOwnerFailure("first", now.Add(-time.Second), now))
	require.NoError(t, err)
	_, err = owner.receive(now, first, engine.ProviderWaitRequest{Delay: 5 * time.Second})
	require.NoError(t, err)
	expiry := first.hold.interval.end
	_, err = owner.receive(now, first, engine.ProviderWaitObserved{StartedAt: now, Through: now})
	require.NoError(t, err)

	_, decisions, err := owner.open(now.Add(time.Second), recoveryOwnerFailure("late", now.Add(-12*time.Second), now))
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	assert.IsType(t, engine.ProviderRecoveryStopped{}, decisions[0].message)
	assert.IsType(t, engine.ProviderRecoveryStopped{}, decisions[1].message)
	assert.Zero(t, owner.budget.Remaining)
	assert.Equal(t, expiry, first.hold.interval.end)
	assert.Equal(t, 12*time.Second, owner.budget.allowance.confirmed.duration())

	decisions, err = owner.receive(now.Add(2*time.Second), first, engine.ProviderWaitFinished{
		StartedAt: now, EndedAt: now.Add(2 * time.Second),
	})
	require.NoError(t, err)
	assert.Empty(t, decisions)
	assert.Nil(t, first.hold)
	assert.Equal(t, 14*time.Second, owner.budget.allowance.confirmed.duration())
	assert.Empty(t, owner.reconsider(now.Add(time.Minute)))
}

func TestProviderRecoveryOwnerExhaustionPreservesResponsibleFailure(t *testing.T) {
	now := time.Unix(100, 0)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: 6 * time.Second})
	failure := recoveryOwnerFailure("first", now.Add(-time.Second), now)
	first, _, err := owner.open(now, failure)
	require.NoError(t, err)
	_, _, err = owner.open(now, recoveryOwnerFailure("second", now, now))
	require.NoError(t, err)
	_, err = owner.receive(now, first, engine.ProviderWaitRequest{Delay: 5 * time.Second})
	require.NoError(t, err)
	decisions, err := owner.receive(now.Add(5*time.Second), first, engine.ProviderWaitObserved{
		StartedAt: now, Through: now.Add(5 * time.Second),
	})
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	actual, ok := model.AsProviderError(owner.stopped)
	require.True(t, ok)
	assert.Same(t, failure.Err, actual)
}

func TestProviderRecoveryOwnerDoesNotRetryAnUnprovenTimeout(t *testing.T) {
	now := time.Unix(100, 0)
	owner, request := recoveryOwnerAtAttempt(t, now)
	timeout := errors.New("planner start-to-close timeout; invocation result unknown")
	held := request.hold
	remaining := owner.budget.Remaining
	decisions, err := owner.receive(now.Add(time.Second), request, engine.ProviderPhaseFailed{Err: timeout})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	stopped, ok := decisions[0].message.(engine.ProviderRecoveryStopped)
	require.True(t, ok)
	assert.Same(t, timeout, stopped.Err)
	assert.Same(t, held, request.hold)
	assert.Equal(t, remaining, owner.budget.Remaining)
	assert.False(t, owner.canCheckpoint())
	decisions, err = owner.receive(now.Add(time.Minute), request, engine.ProviderWaitRequest{Delay: time.Second})
	require.NoError(t, err)
	assert.Empty(t, decisions)
	assert.Same(t, held, request.hold)
}

func TestProviderRecoveryOwnerKeepsWinningStopWhileSettlingLateSuccess(t *testing.T) {
	now := time.Unix(100, 0)
	owner, request := recoveryOwnerAtAttempt(t, now)
	decisions := owner.stop(context.Canceled)
	require.Len(t, decisions, 1)
	require.ErrorIs(t, request.stopped, context.Canceled)
	decisions, err := owner.receive(now.Add(time.Second), request, engine.ProviderAttemptSucceeded{})
	require.NoError(t, err)
	assert.Empty(t, decisions)
	assert.Nil(t, request.hold)
	require.ErrorIs(t, owner.stopped, context.Canceled)

	late, decisions, err := owner.open(now.Add(time.Second), recoveryOwnerFailure("late", now.Add(-time.Minute), now))
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.ErrorIs(t, late.stopped, context.Canceled)
	assert.Zero(t, owner.budget.Remaining)
	assert.Empty(t, owner.reconsider(now.Add(time.Hour)))
}

func TestProviderRecoveryOwnerAcceptsCrossingWaitObservationOnce(t *testing.T) {
	now := time.Unix(100, 0)
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: time.Minute})
	request, _, err := owner.open(now, recoveryOwnerFailure("first", now.Add(-time.Second), now))
	require.NoError(t, err)
	_, err = owner.receive(now, request, engine.ProviderWaitRequest{Delay: 5 * time.Second})
	require.NoError(t, err)
	require.Len(t, owner.observations(), 1)
	closed := engine.ProviderWaitFinished{StartedAt: now, EndedAt: now.Add(5 * time.Second)}
	_, err = owner.receive(closed.EndedAt, request, closed)
	require.NoError(t, err)
	remaining := owner.budget.Remaining

	for _, message := range []engine.ProviderRecoveryMessage{
		engine.ProviderWaitObserved{StartedAt: now, Through: now.Add(time.Second)},
		closed,
	} {
		decisions, err := owner.receive(closed.EndedAt, request, message)
		require.NoError(t, err)
		assert.Empty(t, decisions)
		assert.Equal(t, remaining, owner.budget.Remaining)
	}
	_, err = owner.receive(closed.EndedAt.Add(time.Second), request, engine.ProviderWaitFinished{
		StartedAt: now, EndedAt: closed.EndedAt.Add(time.Second),
	})
	require.ErrorContains(t, err, "conflicting times")
}

func TestProviderRecoveryOwnerChargesLateFinalizationWithoutRenewal(t *testing.T) {
	now := time.Unix(100, 0)
	owner, request := recoveryOwnerAtAttempt(t, now)
	expiry := request.hold.interval.end
	failure := recoveryOwnerFailure("retry", now, expiry.Add(3*time.Second))
	decisions, err := owner.receive(failure.EndedAt, request, engine.ProviderAttemptFailed{Failure: failure})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.IsType(t, engine.ProviderRecoveryStopped{}, decisions[0].message)
	assert.Zero(t, owner.budget.Remaining)
	assert.Nil(t, request.hold)
	assert.Greater(t, owner.budget.allowance.confirmed.duration(), time.Minute)
}

func TestProviderRecoveryOwnerRejectsReusedFailureAndUnissuedAttempt(t *testing.T) {
	now := time.Unix(100, 0)
	owner, request := recoveryOwnerAtAttempt(t, now)
	remaining := owner.budget.Remaining
	reused := recoveryOwnerFailure(request.failure.PublicationBatchID, now, now.Add(time.Second))
	_, err := owner.receive(reused.EndedAt, request, engine.ProviderAttemptFailed{Failure: reused})
	require.ErrorContains(t, err, "reused")
	assert.Equal(t, remaining, owner.budget.Remaining)
	_, err = owner.receive(now, request, engine.ProviderAttemptRequest{})
	require.ErrorContains(t, err, "before wait settlement")
}

func recoveryOwnerAtAttempt(t *testing.T, now time.Time) (*providerRecoveryOwner, *providerRecoveryRequest) {
	t.Helper()
	owner := newProviderRecoveryOwner(&providerRecoveryBudget{Remaining: time.Minute})
	start := now.Add(-2 * time.Second)
	request, _, err := owner.open(start, recoveryOwnerFailure("first", start.Add(-time.Second), start))
	require.NoError(t, err)
	_, err = owner.receive(start, request, engine.ProviderWaitRequest{Delay: 2 * time.Second})
	require.NoError(t, err)
	_, err = owner.receive(now, request, engine.ProviderWaitFinished{StartedAt: start, EndedAt: now})
	require.NoError(t, err)
	_, err = owner.receive(now, request, engine.ProviderAttemptRequest{})
	require.NoError(t, err)
	require.Equal(t, providerRecoveryAttempting, request.phase)
	return owner, request
}

func recoveryOwnerFailure(publication string, start, end time.Time) engine.ProviderRecoveryFailure {
	return engine.ProviderRecoveryFailure{
		PublicationBatchID: publication,
		StartedAt:          start,
		EndedAt:            end,
		Err: model.NewProviderError(
			"synthetic", "stream", 429, model.ProviderErrorKindRateLimited,
			"rate_limit", "retry later", "request", true, nil,
		),
	}
}
