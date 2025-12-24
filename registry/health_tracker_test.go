package registry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
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

			// Directly set a stale timestamp in the health map.
			// stalenessThreshold = (missedPingThreshold + 1) * pingInterval
			stalenessThreshold := time.Duration(missedPingThreshold+1) * pingInterval
			staleTime := time.Now().Add(-stalenessThreshold - time.Second)
			key := "registry:health:" + toolsetName
			if _, err := healthMap.Set(ctx, key, fmt.Sprintf("%d", staleTime.UnixNano())); err != nil {
				return false
			}

			// Should be unhealthy because the timestamp is stale.
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

			// Subscribe to health map events to wait for updates to propagate.
			healthEvents := healthMap.Subscribe()
			defer healthMap.Unsubscribe(healthEvents)

			// Directly set a stale timestamp to make toolset unhealthy.
			// stalenessThreshold = (2 + 1) * 100ms = 300ms
			staleTime := time.Now().Add(-500 * time.Millisecond)
			key := "registry:health:" + toolsetName
			if _, err := healthMap.Set(ctx, key, fmt.Sprintf("%d", staleTime.UnixNano())); err != nil {
				return false
			}

			// Wait for the set to propagate.
			select {
			case <-healthEvents:
			case <-time.After(5 * time.Second):
				return false
			}

			// Should be unhealthy because the timestamp is stale.
			if tracker.IsHealthy(toolsetName) {
				return false
			}

			// Record a pong (updates timestamp to now).
			if err := tracker.RecordPong(ctx, toolsetName); err != nil {
				return false
			}

			// Wait for the pong to propagate.
			select {
			case <-healthEvents:
			case <-time.After(5 * time.Second):
				return false
			}

			// Should be healthy again.
			return tracker.IsHealthy(toolsetName)
		},
		genHealthyToolsetName(),
	))

	properties.TestingRun(t)
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
