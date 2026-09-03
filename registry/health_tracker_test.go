package registry

// These tests pin catalog-owned health epochs independently from Pulse pool
// integration. Redis integration tests cover the same CAS transitions across
// registry replicas.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	recordingHealthStreamManager struct {
		pings int
	}

	authoritativeKeysFailureMap struct {
		catalogMap
		err error
	}
)

var errTestRedisUnavailable = errors.New("redis unavailable")

func TestHealthTrackerRecordsPeriodicHealthSpans(t *testing.T) {
	recorder := newHealthSpanRecorder(t)

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalogMap := newTestCatalogMap()
	catalog := newToolsetCatalog(catalogMap, clock)
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
	streams := &recordingHealthStreamManager{}
	tracker := newDirectHealthTracker(ctx, catalog)
	tracker.streamManager = streams

	// The live provider has not answered a ping, so the first sample is not ready.
	tracker.sampleRegisteredToolset(ctx, "test.toolset")
	require.Len(t, recorder.Ended(), 1)
	assertHealthSpan(t, recorder.Ended()[0], 0, 1, false, 0)
	assert.Equal(t, 1, streams.pings)

	require.NoError(t, tracker.RecordPong(
		ctx,
		"test.toolset",
		"provider-a",
		incarnation,
		newPingID(admission.RegistrationToken, admission.HealthEpoch),
	))
	tracker.sampleRegisteredToolset(ctx, "test.toolset")
	require.Len(t, recorder.Ended(), 2)
	assertHealthSpan(t, recorder.Ended()[1], 1, 1, true, 0)
	assert.Equal(t, 2, streams.pings)

	clock.Set(now.Add(3 * time.Second))
	tracker.sampleRegisteredToolset(ctx, "test.toolset")
	require.Len(t, recorder.Ended(), 3)
	assertHealthSpan(t, recorder.Ended()[2], 0, 1, true, 3000)
	assert.Equal(t, 3, streams.pings)

	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider-a",
		incarnation,
		admission.RegistrationToken,
	))
	tracker.sampleRegisteredToolset(ctx, "test.toolset")
	require.Len(t, recorder.Ended(), 4)
	assertHealthSpan(t, recorder.Ended()[3], 0, 0, false, 0)
	assert.Equal(t, 3, streams.pings, "a toolset without a provider must not receive a ping")

	readErr := errors.New("catalog unavailable")
	catalogMap.testAndSetErr = readErr
	tracker.sampleRegisteredToolset(ctx, "test.toolset")
	require.Len(t, recorder.Ended(), 5)
	failed := recorder.Ended()[4]
	assertHealthSpanHasOnlyToolset(t, failed)
	assert.Equal(t, codes.Error, failed.Status().Code)
	assert.Equal(t, "read toolset health", failed.Status().Description)
	require.Len(t, failed.Events(), 1)
	assert.Equal(t, "exception", failed.Events()[0].Name)

	catalogMap.mu.Lock()
	delete(catalogMap.content, toolsetCatalogKey("test.toolset"))
	catalogMap.mu.Unlock()
	tracker.sampleRegisteredToolset(ctx, "test.toolset")
	require.Len(t, recorder.Ended(), 6)
	missing := recorder.Ended()[5]
	assertHealthSpanHasOnlyToolset(t, missing)
	assert.Equal(t, codes.Unset, missing.Status().Code)
	assert.Empty(t, missing.Events())
	assert.Equal(t, 3, streams.pings)
}

func TestHealthTrackerRecordsFailuresBeforeToolsetSamples(t *testing.T) {
	t.Run("revision repair", func(t *testing.T) {
		recorder := newHealthSpanRecorder(t)
		catalog := newToolsetCatalog(newTestCatalogMap(), newTestTimeSource(time.Unix(1_700_000_000, 0)))
		tracker := newDirectHealthTracker(context.Background(), catalog)
		tracker.redis = newUnavailableRedisClient(t)
		tracker.revisionHashKey = "map:test:content"

		tracker.runHealthSweep(context.Background())

		require.Len(t, recorder.Ended(), 1)
		assertHealthSweepFailure(t, recorder.Ended()[0], "repair_revision", "")
	})

	t.Run("catalog enumeration", func(t *testing.T) {
		recorder := newHealthSpanRecorder(t)
		catalog := newToolsetCatalog(
			authoritativeKeysFailureMap{
				catalogMap: newTestCatalogMap(),
				err:        errors.New("catalog unavailable"),
			},
			newTestTimeSource(time.Unix(1_700_000_000, 0)),
		)
		tracker := newDirectHealthTracker(context.Background(), catalog)

		ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(
			context.Background(),
			"toolregistry.health.sweep",
			trace.WithAttributes(attribute.String("toolregistry.registry", tracker.leaseScope)),
		)
		tracker.pingRegisteredToolsets(ctx)
		span.End()

		require.Len(t, recorder.Ended(), 1)
		assertHealthSweepFailure(t, recorder.Ended()[0], "enumerate_toolsets", "")
	})

	t.Run("ping lease", func(t *testing.T) {
		recorder := newHealthSpanRecorder(t)
		ctx := context.Background()
		clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
		catalog := newToolsetCatalog(newTestCatalogMap(), clock)
		_, err := catalog.Register(
			ctx,
			testCatalogToolset("test.toolset", "test", nil),
			testAdmissionRevisionA,
			"provider-a",
			uuid.NewString(),
			time.Minute,
		)
		require.NoError(t, err)
		tracker := newDirectHealthTracker(ctx, catalog)
		tracker.redis = newUnavailableRedisClient(t)

		ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(
			ctx,
			"toolregistry.health.sweep",
			trace.WithAttributes(attribute.String("toolregistry.registry", tracker.leaseScope)),
		)
		tracker.pingRegisteredToolsets(ctx)
		span.End()

		require.Len(t, recorder.Ended(), 1)
		assertHealthSweepFailure(t, recorder.Ended()[0], "acquire_lease", "test.toolset")
	})
}

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

func newDirectHealthTracker(_ context.Context, catalog *toolsetCatalog) *healthTracker {
	closed := make(chan struct{})
	close(closed)
	return &healthTracker{
		catalog:            catalog,
		catalogMap:         catalog.m,
		leaseScope:         "test",
		pingInterval:       time.Second,
		stalenessThreshold: 2 * time.Second,
		revFloors:          make(map[string]int64),
		closeCh:            make(chan struct{}),
		doneCh:             closed,
	}
}

func newHealthSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previousProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}

func newUnavailableRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Dialer:     rejectRedisConnection,
		MaxRetries: -1,
	})
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})
	return client
}

func assertHealthSpan(
	t *testing.T,
	span sdktrace.ReadOnlySpan,
	ready, providers int64,
	pongSeen bool,
	ageMillis int64,
) {
	t.Helper()
	attrs := healthSpanAttributes(span)
	assert.Equal(t, "toolregistry.health", span.Name())
	assert.Equal(t, "test", attrs["toolregistry.registry"].AsString())
	assert.Equal(t, "test.toolset", attrs["toolregistry.toolset"].AsString())
	assert.Equal(t, ready, attrs["toolregistry.ready"].AsInt64())
	assert.Equal(t, providers, attrs["toolregistry.provider_count"].AsInt64())
	assert.Equal(t, pongSeen, attrs["toolregistry.pong_seen"].AsBool())
	assert.Equal(t, int64(2000), attrs["toolregistry.staleness_threshold_ms"].AsInt64())
	if pongSeen {
		assert.Equal(t, ageMillis, attrs["toolregistry.last_pong_age_ms"].AsInt64())
	} else {
		assert.NotContains(t, attrs, attribute.Key("toolregistry.last_pong_age_ms"))
	}
	assert.Equal(t, codes.Ok, span.Status().Code)
	assert.Empty(t, span.Events())
}

func assertHealthSpanHasOnlyToolset(t *testing.T, span sdktrace.ReadOnlySpan) {
	t.Helper()
	attrs := healthSpanAttributes(span)
	assert.Equal(t, "toolregistry.health", span.Name())
	require.Len(t, attrs, 2)
	assert.Equal(t, "test", attrs["toolregistry.registry"].AsString())
	assert.Equal(t, "test.toolset", attrs["toolregistry.toolset"].AsString())
	assert.NotContains(t, attrs, attribute.Key("toolregistry.ready"))
	assert.NotContains(t, attrs, attribute.Key("toolregistry.provider_count"))
}

func assertHealthSweepFailure(t *testing.T, span sdktrace.ReadOnlySpan, step, toolset string) {
	t.Helper()
	assert.Equal(t, "toolregistry.health.sweep", span.Name())
	attrs := healthSpanAttributes(span)
	require.Len(t, attrs, 1)
	assert.Equal(t, "test", attrs["toolregistry.registry"].AsString())
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, "health sweep failed", span.Status().Description)
	require.Len(t, span.Events(), 1)
	event := span.Events()[0]
	assert.Equal(t, "exception", event.Name)
	eventAttrs := make(map[attribute.Key]attribute.Value, len(event.Attributes))
	for _, attr := range event.Attributes {
		eventAttrs[attr.Key] = attr.Value
	}
	assert.Equal(t, step, eventAttrs["toolregistry.step"].AsString())
	if toolset == "" {
		assert.NotContains(t, eventAttrs, attribute.Key("toolregistry.toolset"))
		return
	}
	assert.Equal(t, toolset, eventAttrs["toolregistry.toolset"].AsString())
}

func healthSpanAttributes(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	attrs := make(map[attribute.Key]attribute.Value, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		attrs[attr.Key] = attr.Value
	}
	return attrs
}

func (m *recordingHealthStreamManager) GetOrCreateStream(context.Context, string) (clientspulse.Stream, string, error) {
	return nil, "", nil
}

func (m *recordingHealthStreamManager) PublishToolCall(context.Context, string, toolregistry.ToolCallMessage) error {
	m.pings++
	return nil
}

func (m *recordingHealthStreamManager) PublishAdmittedToolCall(
	context.Context,
	string,
	toolregistry.ToolCallMessage,
	callAdmission,
	string,
) error {
	return nil
}

func (m authoritativeKeysFailureMap) AuthoritativeKeys(context.Context) ([]string, error) {
	return nil, m.err
}

func rejectRedisConnection(context.Context, string, string) (net.Conn, error) {
	return nil, errTestRedisUnavailable
}
