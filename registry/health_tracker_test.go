package registry

// These tests pin catalog-owned health epochs independently from Pulse pool
// integration. Redis integration tests cover the same CAS transitions across
// registry replicas.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestHealthTrackerPongIsMonotonicAndIncarnationFenced(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	incarnation := uuid.NewString()
	admission, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider-a",
		incarnation,
		time.Minute,
	)
	require.NoError(t, err)
	tracker := newDirectHealthTracker(ctx, catalog)
	pingID := newPingID(admission.RegistrationToken, admission.HealthEpoch)

	require.NoError(t, tracker.RecordPong(ctx, "test.toolset", "provider-a", incarnation, pingID))
	health, err := tracker.Health(ctx, "test.toolset", admission.RegistrationToken)
	require.NoError(t, err)
	assert.Equal(t, now, health.LastPong)
	assert.True(t, health.Healthy)

	clock.Set(now.Add(-time.Minute))
	require.NoError(t, tracker.RecordPong(ctx, "test.toolset", "provider-a", incarnation, pingID))
	entry, _, err := catalog.healthEntry(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Equal(t, now.UnixNano(), entry.LastPongUnixNano)

	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider-a",
		incarnation,
		admission.RegistrationToken,
	))
	require.NoError(t, tracker.RecordPong(ctx, "test.toolset", "provider-a", incarnation, pingID))
	entry, _, err = catalog.healthEntry(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Zero(t, entry.LastPongUnixNano)
	assert.Equal(t, admission.HealthEpoch+1, entry.HealthEpoch)
}

func TestHealthTrackerZeroLeaseReregistrationRejectsOldPong(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	firstIncarnation := uuid.NewString()
	first, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider",
		firstIncarnation,
		time.Minute,
	)
	require.NoError(t, err)
	oldPing := newPingID(first.RegistrationToken, first.HealthEpoch)
	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider",
		firstIncarnation,
		first.RegistrationToken,
	))
	secondIncarnation := uuid.NewString()
	second, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider",
		secondIncarnation,
		time.Minute,
	)
	require.NoError(t, err)
	assert.Greater(t, second.HealthEpoch, first.HealthEpoch)
	tracker := newDirectHealthTracker(ctx, catalog)

	require.NoError(t, tracker.RecordPong(ctx, "test.toolset", "provider", firstIncarnation, oldPing))
	health, err := tracker.Health(ctx, "test.toolset", second.RegistrationToken)
	require.NoError(t, err)
	assert.False(t, health.Healthy)

	require.NoError(t, tracker.RecordPong(
		ctx,
		"test.toolset",
		"provider",
		secondIncarnation,
		newPingID(second.RegistrationToken, second.HealthEpoch),
	))
	health, err = tracker.Health(ctx, "test.toolset", second.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, health.Healthy)
}

func newDirectHealthTracker(ctx context.Context, catalog *toolsetCatalog) *healthTracker {
	lifecycleCtx := context.WithoutCancel(ctx)
	return &healthTracker{
		catalog:             catalog,
		catalogMap:          catalog.m,
		pingInterval:        time.Second,
		missedPingThreshold: 1,
		stalenessThreshold:  2 * time.Second,
		logger:              telemetry.NewNoopLogger(),
		lifecycleCtx:        lifecycleCtx,
		lifecycleCancel:     func() {},
		tickers:             make(map[string]healthTicker),
		cancels:             make(map[string]context.CancelFunc),
		tickerIdentities:    make(map[string]string),
		nextRepair:          make(map[string]time.Time),
		lastObservedHealthy: make(map[string]bool),
		lastObservedPong:    make(map[string]int64),
	}
}
