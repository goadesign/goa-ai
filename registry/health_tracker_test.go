package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/pool"
	"goa.design/pulse/rmap"
)

// iterCounter provides unique IDs for each property test iteration.
var iterCounter atomic.Int64

// TestUnhealthyToolsetFastFailure verifies Property 9: Unhealthy toolset fast failure.
// **Feature: internal-tool-registry, Property 9: Unhealthy toolset fast failure**
// *For any* toolset where all providers are marked unhealthy, CallTool should
// immediately return service unavailable without waiting for timeout.
// **Validates: Requirements 9.5, 13.4**
//
// This test verifies the health tracker correctly determines health based on
// the staleness threshold. We test the core logic by directly manipulating
// timestamps in the health map rather than relying on timing.
func TestUnhealthyToolsetFastFailure(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("toolset is unhealthy when last pong exceeds staleness threshold", prop.ForAll(
		func(toolsetName string, missedPingThreshold int) bool {
			// Use unique suffix per iteration to avoid conflicts.
			iter := iterCounter.Add(1)
			suffix := fmt.Sprintf("%s-%d", toolsetName, iter)

			// Create unique rmaps for this test iteration.
			healthMap, err := rmap.Join(ctx, "health-test-"+suffix, rdb)
			if err != nil {
				return false
			}
			defer healthMap.Close()

			registryMap, err := rmap.Join(ctx, "registry-test-"+suffix, rdb)
			if err != nil {
				return false
			}
			defer registryMap.Close()
			registryEvents := registryMap.Subscribe()
			defer registryMap.Unsubscribe(registryEvents)
			healthEvents := healthMap.Subscribe()
			defer healthMap.Unsubscribe(healthEvents)

			// Create a pool node for distributed tickers.
			node, err := pool.AddNode(ctx, "health-test-pool-"+suffix, rdb, testNodeOpts()...)
			if err != nil {
				return false
			}
			defer func() { _ = node.Close(ctx) }()

			mockSM := newMockStreamManager()

			pingInterval := 100 * time.Millisecond
			tracker, err := NewHealthTracker(
				mockSM,
				healthMap,
				registryMap,
				node,
				WithPingInterval(pingInterval),
				WithMissedPingThreshold(missedPingThreshold),
			)
			if err != nil {
				return false
			}
			defer func() { _ = tracker.Close() }()

			catalog := newToolsetCatalog(registryMap)
			if err := saveHealthTestToolset(ctx, catalog, toolsetName, "registration-1"); err != nil {
				return false
			}
			awaitMapEvent(registryEvents)
			registrationToken, err := catalog.RegistrationToken(ctx, toolsetName)
			if err != nil {
				return false
			}

			// Directly set a stale timestamp in the health map.
			// stalenessThreshold = (missedPingThreshold + 1) * pingInterval
			stalenessThreshold := time.Duration(missedPingThreshold+1) * pingInterval
			staleTime := time.Now().Add(-stalenessThreshold - time.Second)
			if err := setHealthRecordForTest(ctx, healthMap, toolsetName, registrationToken, staleTime); err != nil {
				return false
			}
			awaitMapEvent(healthEvents)
			return !tracker.IsHealthy(toolsetName)
		},
		genHealthyToolsetName(),
		genMissedPingThreshold(),
	))

	properties.TestingRun(t)
}

// TestPongRestoresHealthyStatus verifies that responding to a ping restores healthy status.
// **Feature: internal-tool-registry, Property 9: Unhealthy toolset fast failure**
// This is a complementary test that verifies pong responses restore health.
// **Validates: Requirements 13.5**
func TestPongRestoresHealthyStatus(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("pong response restores healthy status", prop.ForAll(
		func(toolsetName string) bool {
			// Use unique suffix per iteration to avoid conflicts.
			iter := iterCounter.Add(1)
			suffix := fmt.Sprintf("%s-%d", toolsetName, iter)

			healthMap, err := rmap.Join(ctx, "health-pong-test-"+suffix, rdb)
			if err != nil {
				return false
			}
			defer healthMap.Close()

			registryMap, err := rmap.Join(ctx, "registry-pong-test-"+suffix, rdb)
			if err != nil {
				return false
			}
			defer registryMap.Close()
			registryEvents := registryMap.Subscribe()
			defer registryMap.Unsubscribe(registryEvents)

			node, err := pool.AddNode(ctx, "health-pong-pool-"+suffix, rdb, testNodeOpts()...)
			if err != nil {
				return false
			}
			defer func() { _ = node.Close(ctx) }()

			mockSM := newMockStreamManager()

			pingInterval := 100 * time.Millisecond
			tracker, err := NewHealthTracker(
				mockSM,
				healthMap,
				registryMap,
				node,
				WithPingInterval(pingInterval),
				WithMissedPingThreshold(2),
			)
			if err != nil {
				return false
			}
			defer func() { _ = tracker.Close() }()

			catalog := newToolsetCatalog(registryMap)
			if err := saveHealthTestToolset(ctx, catalog, toolsetName, "registration-1"); err != nil {
				return false
			}
			awaitMapEvent(registryEvents)
			registrationToken, err := catalog.RegistrationToken(ctx, toolsetName)
			if err != nil {
				return false
			}

			// Subscribe to health map events to wait for updates to propagate.
			healthEvents := healthMap.Subscribe()
			defer healthMap.Unsubscribe(healthEvents)

			// Directly set a stale timestamp to make toolset unhealthy.
			// stalenessThreshold = (2 + 1) * 100ms = 300ms
			staleTime := time.Now().Add(-500 * time.Millisecond)
			if err := setHealthRecordForTest(ctx, healthMap, toolsetName, registrationToken, staleTime); err != nil {
				return false
			}
			awaitMapEvent(healthEvents)

			// Should be unhealthy because the timestamp is stale.
			if tracker.IsHealthy(toolsetName) {
				return false
			}

			// Record a pong (updates timestamp to now).
			if err := tracker.RecordPong(ctx, toolsetName, newPingID(registrationToken)); err != nil {
				return false
			}
			awaitMapEvent(healthEvents)

			// Should be healthy again.
			return tracker.IsHealthy(toolsetName)
		},
		genHealthyToolsetName(),
	))

	properties.TestingRun(t)
}

func TestPongForUnregisteredToolsetDoesNotCreateHealth(t *testing.T) {
	ctx := context.Background()
	svc, tracker, _, healthMap, _ := newPongTestService(t)
	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:  "registration-1/ping-1",
		Toolset: "unknown-toolset",
	}))

	health, err := tracker.Health("unknown-toolset")
	require.NoError(t, err)
	require.False(t, health.Healthy)

	_, ok := healthMap.Get(healthKey("unknown-toolset"))
	require.False(t, ok)
}

func TestReregisterInvalidatesPreviousRegistrationHealth(t *testing.T) {
	ctx := context.Background()
	svc, tracker, catalog, healthMap, registryMap := newPongTestService(t)
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)
	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         "toolset-1",
		RegisteredAt: "registration-1",
	}))
	awaitMapEvent(registryEvents)
	firstToken := requireRegistrationToken(t, ctx, catalog)
	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:  newPingID(firstToken),
		Toolset: "toolset-1",
	}))
	awaitMapEvent(healthEvents)
	health, err := tracker.Health("toolset-1")
	require.NoError(t, err)
	require.True(t, health.Healthy)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         "toolset-1",
		RegisteredAt: "registration-2",
	}))
	awaitMapEvent(registryEvents)
	secondToken := requireRegistrationToken(t, ctx, catalog)
	require.NotEqual(t, firstToken, secondToken)

	health, err = tracker.Health("toolset-1")
	require.NoError(t, err)
	require.False(t, health.Healthy)

	healthBefore, ok := healthMap.Get(healthKey("toolset-1"))
	require.True(t, ok)

	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:  newPingID(firstToken),
		Toolset: "toolset-1",
	}))
	healthAfter, ok := healthMap.Get(healthKey("toolset-1"))
	require.True(t, ok)
	require.Equal(t, healthBefore, healthAfter)

	health, err = tracker.Health("toolset-1")
	require.NoError(t, err)
	require.False(t, health.Healthy)
}

func TestReregisterWithCollidingRegistrationTimestampsRejectsStalePong(t *testing.T) {
	ctx := context.Background()
	svc, tracker, catalog, healthMap, registryMap := newPongTestService(t)
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)
	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	const (
		toolset      = "toolset-1"
		registeredAt = "2024-01-15T10:30:00Z"
	)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         toolset,
		RegisteredAt: registeredAt,
	}))
	awaitMapEvent(registryEvents)
	firstToken := requireRegistrationToken(t, ctx, catalog)
	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:  newPingID(firstToken),
		Toolset: toolset,
	}))
	awaitMapEvent(healthEvents)
	health, err := tracker.Health(toolset)
	require.NoError(t, err)
	require.True(t, health.Healthy)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         toolset,
		RegisteredAt: registeredAt,
	}))
	awaitMapEvent(registryEvents)
	secondToken := requireRegistrationToken(t, ctx, catalog)
	require.NotEqual(t, firstToken, secondToken)

	health, err = tracker.Health(toolset)
	require.NoError(t, err)
	require.False(t, health.Healthy)

	healthBefore, ok := healthMap.Get(healthKey(toolset))
	require.True(t, ok)

	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:  newPingID(firstToken),
		Toolset: toolset,
	}))
	healthAfter, ok := healthMap.Get(healthKey(toolset))
	require.True(t, ok)
	require.Equal(t, healthBefore, healthAfter)

	health, err = tracker.Health(toolset)
	require.NoError(t, err)
	require.False(t, health.Healthy)
}

// newPongTestService builds a registry service backed by a real health tracker
// so Pong tests exercise the full health admission path.
func newPongTestService(t *testing.T) (*Service, HealthTracker, *toolsetCatalog, *rmap.Map, *rmap.Map) {
	t.Helper()

	rdb := getRedis(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%s-%d", t.Name(), iterCounter.Add(1))

	healthMap, err := rmap.Join(ctx, "health-pong-service-"+suffix, rdb)
	require.NoError(t, err)
	t.Cleanup(func() {
		healthMap.Close()
	})

	registryMap, err := rmap.Join(ctx, "registry-pong-service-"+suffix, rdb)
	require.NoError(t, err)
	t.Cleanup(func() {
		registryMap.Close()
	})

	node, err := pool.AddNode(ctx, "health-pong-service-pool-"+suffix, rdb, testNodeOpts()...)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, node.Close(ctx))
	})

	tracker, err := NewHealthTracker(
		newMockStreamManager(),
		healthMap,
		registryMap,
		node,
		WithPingInterval(100*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, tracker.Close())
	})

	catalog := newToolsetCatalog(registryMap)
	svc, err := newService(serviceOptions{
		catalog:       catalog,
		StreamManager: newMockStreamManagerForService(),
		HealthTracker: tracker,
		PulseClient:   mockpulse.NewClient(t),
	})
	require.NoError(t, err)
	return svc, tracker, catalog, healthMap, registryMap
}

func saveHealthTestToolset(ctx context.Context, catalog *toolsetCatalog, toolset string, registeredAt string) error {
	return catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         toolset,
		RegisteredAt: registeredAt,
	})
}

func requireRegistrationToken(t *testing.T, ctx context.Context, catalog *toolsetCatalog) string {
	t.Helper()

	token, err := catalog.RegistrationToken(ctx, "toolset-1")
	require.NoError(t, err)
	return token
}

// awaitMapEvent blocks until the subscribed replicated map publishes the next
// change. Tests use it to synchronize on actual distributed state updates
// instead of polling local reads.
func awaitMapEvent(events <-chan rmap.EventKind) {
	<-events
}

func setHealthRecordForTest(ctx context.Context, healthMap *rmap.Map, toolset string, registrationToken string, lastPong time.Time) error {
	payload, err := json.Marshal(healthRecord{
		RegistrationToken: registrationToken,
		LastPongUnixNano:  lastPong.UnixNano(),
	})
	if err != nil {
		return err
	}
	_, err = healthMap.Set(ctx, healthKey(toolset), string(payload))
	return err
}

type mockStreamManager struct {
	mu       sync.RWMutex
	messages map[string][]toolregistry.ToolCallMessage
}

func newMockStreamManager() *mockStreamManager {
	return &mockStreamManager{
		messages: make(map[string][]toolregistry.ToolCallMessage),
	}
}

func (m *mockStreamManager) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
	return nil, "mock-stream:" + toolset, nil
}

func (m *mockStreamManager) GetStream(toolset string) clientspulse.Stream {
	return nil
}

func (m *mockStreamManager) RemoveStream(toolset string) {}

func (m *mockStreamManager) PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[toolset] = append(m.messages[toolset], msg)
	return nil
}

func genHealthyToolsetName() gopter.Gen {
	return gen.OneConstOf(
		"data-tools",
		"analytics",
		"etl-pipeline",
		"search-service",
		"notification-tools",
	)
}

func genMissedPingThreshold() gopter.Gen {
	return gen.IntRange(1, 5)
}

var _ StreamManager = (*mockStreamManager)(nil)
