//go:build integration

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/pool"
	"goa.design/pulse/rmap"
)

// iterCounter provides unique suffixes so concurrent tests never share
// replicated map names on the shared Redis container.
var iterCounter atomic.Int64

func TestPongForUnregisteredToolsetDoesNotCreateHealth(t *testing.T) {
	ctx := context.Background()
	svc, tracker, _, healthMap, _ := newPongTestService(t)
	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:     "registration-1/ping-1",
		Toolset:    "unknown-toolset",
		ProviderID: "provider-a",
	}))

	health, err := tracker.Health("unknown-toolset")
	require.NoError(t, err)
	require.False(t, health.Healthy)

	_, ok := healthMap.Get(healthKey("unknown-toolset", "provider-a"))
	require.False(t, ok)
}

func TestToolsetHealthyWhenAnyProviderInstanceFresh(t *testing.T) {
	ctx := context.Background()
	_, tracker, catalog, healthMap, registryMap := newPongTestService(t)
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)
	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         "toolset-1",
		RegisteredAt: "registration-1",
	}))
	awaitMapEvent(registryEvents)
	token := requireRegistrationToken(t, ctx, catalog)
	require.NoError(t, setHealthRecordForTest(ctx, healthMap, "toolset-1", "provider-a", token, time.Now().Add(-time.Hour)))
	awaitMapEvent(healthEvents)

	require.NoError(t, setHealthRecordForTest(ctx, healthMap, "toolset-1", "provider-b", token, time.Now()))
	awaitMapEvent(healthEvents)

	health, err := tracker.Health("toolset-1")
	require.NoError(t, err)
	require.True(t, health.Healthy)
	require.Equal(t, "provider-b", health.ProviderID)
	require.Equal(t, 2, health.ProviderCount)
	require.Equal(t, 1, health.HealthyProviderCount)
}

func TestReregisterSameSchemaPreservesHealth(t *testing.T) {
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
		PingID:     newPingID(firstToken),
		Toolset:    toolset,
		ProviderID: "provider-a",
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
	require.Equal(t, firstToken, secondToken)

	health, err = tracker.Health(toolset)
	require.NoError(t, err)
	require.True(t, health.Healthy)
	require.Equal(t, "provider-a", health.ProviderID)

	require.NoError(t, tracker.RegisterProvider(ctx, toolset, "provider-a"))
	awaitMapEvent(healthEvents)
	health, err = tracker.Health(toolset)
	require.NoError(t, err)
	require.True(t, health.Healthy)
	require.Equal(t, "provider-a", health.ProviderID)
}

func TestReregisterChangedSchemaRequiresNewProviderPong(t *testing.T) {
	ctx := context.Background()
	svc, tracker, catalog, healthMap, registryMap := newPongTestService(t)
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)
	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	const toolset = "toolset-1"
	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         toolset,
		RegisteredAt: "registration-1",
	}))
	awaitMapEvent(registryEvents)
	firstToken := requireRegistrationToken(t, ctx, catalog)
	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:     newPingID(firstToken),
		Toolset:    toolset,
		ProviderID: "provider-a",
	}))
	awaitMapEvent(healthEvents)
	health, err := tracker.Health(toolset)
	require.NoError(t, err)
	require.True(t, health.Healthy)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         toolset,
		Tags:         []string{"changed"},
		RegisteredAt: "registration-2",
	}))
	awaitMapEvent(registryEvents)
	secondToken := requireRegistrationToken(t, ctx, catalog)
	require.NotEqual(t, firstToken, secondToken)

	health, err = tracker.Health(toolset)
	require.NoError(t, err)
	require.False(t, health.Healthy)

	require.NoError(t, svc.Pong(ctx, &genregistry.PongPayload{
		PingID:     newPingID(secondToken),
		Toolset:    toolset,
		ProviderID: "provider-b",
	}))
	awaitMapEvent(healthEvents)
	health, err = tracker.Health(toolset)
	require.NoError(t, err)
	require.True(t, health.Healthy)
	require.Equal(t, "provider-b", health.ProviderID)
}

func TestStaleProviderHealthRecordsArePruned(t *testing.T) {
	ctx := context.Background()
	_, tracker, catalog, healthMap, registryMap := newPongTestService(t)
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)
	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         "toolset-1",
		RegisteredAt: "registration-1",
	}))
	awaitMapEvent(registryEvents)
	token := requireRegistrationToken(t, ctx, catalog)
	healthTracker := tracker.(*healthTracker)

	require.NoError(t, setHealthRecordForTest(ctx, healthMap, "toolset-1", "provider-old", token, time.Now().Add(-time.Hour)))
	awaitMapEvent(healthEvents)
	require.NoError(t, setHealthRecordForTest(ctx, healthMap, "toolset-1", "provider-fresh", token, time.Now()))
	awaitMapEvent(healthEvents)

	healthTracker.observeHealth(ctx, "toolset-1")

	health, err := tracker.Health("toolset-1")
	require.NoError(t, err)
	require.True(t, health.Healthy)
	require.Equal(t, "provider-fresh", health.ProviderID)
	waitForMapKeyRemoval(t, healthMap, healthKey("toolset-1", "provider-old"))
	_, ok := healthMap.Get(healthKey("toolset-1", "provider-fresh"))
	require.True(t, ok)
}

func TestDeleteHealthRecordsRemovesProviderAndLegacyKeys(t *testing.T) {
	ctx := context.Background()
	_, tracker, catalog, healthMap, registryMap := newPongTestService(t)
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	require.NoError(t, catalog.SaveToolset(ctx, &genregistry.Toolset{
		Name:         "toolset-1",
		RegisteredAt: "registration-1",
	}))
	awaitMapEvent(registryEvents)
	token := requireRegistrationToken(t, ctx, catalog)
	require.NoError(t, setHealthRecordForTest(ctx, healthMap, "toolset-1", "provider-a", token, time.Now()))
	_, err := healthMap.Set(ctx, legacyHealthKey("toolset-1"), "legacy-health-record")
	require.NoError(t, err)

	require.NoError(t, tracker.(*healthTracker).deleteHealthRecords(ctx, "toolset-1"))

	waitForMapKeyRemoval(t, healthMap, healthKey("toolset-1", "provider-a"))
	waitForMapKeyRemoval(t, healthMap, legacyHealthKey("toolset-1"))
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

	// A deliberately wide staleness window (5m) keeps "recent pong => healthy"
	// immune to Redis and scheduler latency: these tests verify pong/token/
	// record plumbing, not freshness classification, which is unit-tested on
	// computeToolsetHealth. Tests needing stale records seed timestamps far in
	// the past instead of waiting for wall clock to pass.
	tracker, err := NewHealthTracker(
		newMockStreamManager(),
		healthMap,
		registryMap,
		node,
		WithPingInterval(time.Minute),
		WithMissedPingThreshold(4),
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

func setHealthRecordForTest(ctx context.Context, healthMap *rmap.Map, toolset, providerID, registrationToken string, lastPong time.Time) error {
	payload, err := json.Marshal(healthRecord{
		ProviderID:         providerID,
		RegistrationToken:  registrationToken,
		RegisteredUnixNano: lastPong.UnixNano(),
		LastPongUnixNano:   lastPong.UnixNano(),
	})
	if err != nil {
		return err
	}
	_, err = healthMap.Set(ctx, healthKey(toolset, providerID), string(payload))
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

var _ StreamManager = (*mockStreamManager)(nil)
