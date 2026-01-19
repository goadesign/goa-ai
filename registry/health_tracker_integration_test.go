package registry

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/pool"
	"goa.design/pulse/rmap"
)

var (
	testRedisClient    *redis.Client
	testRedisContainer testcontainers.Container
	skipIntegration    bool
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Start Redis container once for all tests.
	var containerErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				containerErr = fmt.Errorf("docker not available: %v", r)
			}
		}()
		req := testcontainers.ContainerRequest{
			Image:        "redis:7-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		}
		testRedisContainer, containerErr = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
	}()

	if containerErr != nil {
		fmt.Printf("Docker not available, integration tests will be skipped: %v\n", containerErr)
		skipIntegration = true
	} else {
		host, err := testRedisContainer.Host(ctx)
		if err != nil {
			fmt.Printf("Failed to get container host: %v\n", err)
			skipIntegration = true
		} else {
			port, err := testRedisContainer.MappedPort(ctx, "6379")
			if err != nil {
				fmt.Printf("Failed to get container port: %v\n", err)
				skipIntegration = true
			} else {
				testRedisClient = redis.NewClient(&redis.Options{
					Addr: host + ":" + port.Port(),
				})
				if err := testRedisClient.Ping(ctx).Err(); err != nil {
					fmt.Printf("Failed to ping redis: %v\n", err)
					skipIntegration = true
				}
			}
		}
	}

	code := m.Run()

	// Cleanup.
	if testRedisClient != nil {
		_ = testRedisClient.Close()
	}
	if testRedisContainer != nil {
		_ = testRedisContainer.Terminate(ctx)
	}

	os.Exit(code)
}

// getRedis returns the shared Redis client and flushes the database for test isolation.
// Skips the test if Docker/Redis is not available.
func getRedis(t *testing.T) *redis.Client {
	t.Helper()
	if skipIntegration {
		t.Skip("Docker not available, skipping integration test")
	}
	// Flush database for test isolation.
	if err := testRedisClient.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("failed to flush redis: %v", err)
	}
	return testRedisClient
}

// testNodeOpts returns pool node options optimized for fast test execution.
// WithJobSinkBlockDuration controls how long node.Close() blocks waiting for jobs.
// The default is 5s which causes slow test cleanup.
func testNodeOpts() []pool.NodeOption {
	return []pool.NodeOption{
		// Use small TTLs so worker disappearance and job failover are prompt and
		// reliable in CI. Defaults (workerTTL=30s, ackGracePeriod=20s) make the
		// failover tests nondeterministic at typical timeouts.
		pool.WithWorkerTTL(1 * time.Second),
		pool.WithAckGracePeriod(200 * time.Millisecond),
		pool.WithWorkerShutdownTTL(2 * time.Second),
		pool.WithJobSinkBlockDuration(100 * time.Millisecond),
	}
}

// TestMultiNodeRegistrationSync verifies that when a toolset is registered on one node,
// all other nodes become aware and start participating in the distributed ticker.
func TestMultiNodeRegistrationSync(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Use unique map/pool names per test to ensure isolation.
	healthMap, err := rmap.Join(ctx, "health-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	// Subscribe to registry changes before creating trackers.
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	// Create two pool nodes in the same pool simulating two gateway instances.
	poolName := "pool-" + t.Name()
	node1, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	defer func() { _ = node1.Close(ctx) }()

	node2, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node2: %v", err)
	}
	defer func() { _ = node2.Close(ctx) }()

	mockSM := newMockStreamManager()

	// Create two health trackers (simulating two gateway nodes).
	tracker1, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node1,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker1: %v", err)
	}
	defer func() { _ = tracker1.Close() }()

	tracker2, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node2,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker2: %v", err)
	}
	defer func() { _ = tracker2.Close() }()

	// Register a toolset on node 1.
	if err := tracker1.StartPingLoop(ctx, "test-toolset"); err != nil {
		t.Fatalf("failed to start ping loop: %v", err)
	}

	// Wait for the registry event and for the peer node to start local ticker
	// participation for this toolset.
	select {
	case <-registryEvents:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for registry event")
	}
	waitForTicker(t, tracker2, "test-toolset")

	// A toolset is healthy only after a provider Pong; StartPingLoop must not
	// treat registration as provider liveness.
	if tracker1.IsHealthy("test-toolset") {
		t.Error("tracker1 should see toolset as unhealthy before any pong")
	}
	if tracker2.IsHealthy("test-toolset") {
		t.Error("tracker2 should see toolset as unhealthy before any pong")
	}

	// Verify the toolset is in the registry map.
	_, ok := registryMap.Get("registry:toolsets:test-toolset")
	if !ok {
		t.Error("toolset should be in registry map")
	}
}

// TestMultiNodeUnregistrationSync verifies that when a toolset is unregistered on one node,
// all other nodes stop their ticker participation.
func TestMultiNodeUnregistrationSync(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Use unique map names to avoid interference from previous test's cached state.
	healthMap, err := rmap.Join(ctx, "health-unreg-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-unreg-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	poolName := "pool-" + t.Name()
	node1, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	defer func() { _ = node1.Close(ctx) }()

	node2, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node2: %v", err)
	}
	defer func() { _ = node2.Close(ctx) }()

	mockSM := newMockStreamManager()

	tracker1, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node1,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker1: %v", err)
	}
	defer func() { _ = tracker1.Close() }()

	tracker2, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node2,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker2: %v", err)
	}
	defer func() { _ = tracker2.Close() }()

	// Register on node 1.
	if err := tracker1.StartPingLoop(ctx, "test-toolset"); err != nil {
		t.Fatalf("failed to start ping loop: %v", err)
	}

	// Wait for registration event.
	select {
	case <-registryEvents:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for registration event")
	}

	// Subscribe to health map changes to detect cleanup.
	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	// Unregister on node 2 (different node than registration).
	tracker2.StopPingLoop(ctx, "test-toolset")

	// Wait for both unregistration event (registry map) and health cleanup event.
	// They may arrive in any order, so we wait for both.
	gotRegistryEvent := false
	gotHealthEvent := false
	timeout := time.After(5 * time.Second)
	for !gotRegistryEvent || !gotHealthEvent {
		select {
		case <-registryEvents:
			gotRegistryEvent = true
		case <-healthEvents:
			gotHealthEvent = true
		case <-timeout:
			t.Fatalf("timeout waiting for events (registry=%v, health=%v)", gotRegistryEvent, gotHealthEvent)
		}
	}

	// Toolset should be removed from registry map.
	_, ok := registryMap.Get("registry:toolsets:test-toolset")
	if ok {
		t.Error("toolset should be removed from registry map")
	}

	// Health state should be cleaned up.
	_, ok = healthMap.Get("registry:health:test-toolset")
	if ok {
		t.Error("health state should be cleaned up")
	}
}

// TestNewNodeSyncsExistingToolsets verifies that a new node joining the cluster
// syncs with existing registered toolsets.
func TestNewNodeSyncsExistingToolsets(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	poolName := "pool-" + t.Name()
	node1, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	defer func() { _ = node1.Close(ctx) }()

	mockSM := newMockStreamManager()

	// Create first tracker and register a toolset.
	tracker1, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node1,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker1: %v", err)
	}
	defer func() { _ = tracker1.Close() }()

	if err := tracker1.StartPingLoop(ctx, "existing-toolset"); err != nil {
		t.Fatalf("failed to start ping loop: %v", err)
	}

	// Wait for registration event.
	select {
	case <-registryEvents:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for registration event")
	}

	// Now create a second node (simulating a new gateway joining).
	node2, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node2: %v", err)
	}
	defer func() { _ = node2.Close(ctx) }()

	// The new tracker should sync existing toolsets on creation.
	tracker2, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node2,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker2: %v", err)
	}
	defer func() { _ = tracker2.Close() }()

	waitForTicker(t, tracker2, "existing-toolset")
	if tracker2.IsHealthy("existing-toolset") {
		t.Error("tracker2 should see existing toolset as unhealthy before any pong")
	}
}

func waitForTicker(t *testing.T, tracker HealthTracker, toolset string) {
	t.Helper()

	ht, ok := tracker.(*healthTracker)
	if !ok {
		t.Fatalf("unexpected tracker type %T", tracker)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		ht.mu.Lock()
		_, hasTicker := ht.tickers[toolset]
		ht.mu.Unlock()
		if hasTicker {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("tracker did not start local ticker for %q", toolset)
		}
	}
}

// TestPingsContinueAfterNodeFailure verifies that pings continue when the
// node that was sending pings crashes (simulated by closing the node without
// gracefully stopping the ticker).
func TestPingsContinueAfterNodeFailure(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	poolName := "pool-" + t.Name()
	node1, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}

	node2, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node2: %v", err)
	}
	defer func() { _ = node2.Close(ctx) }()

	mockSM := &pingCountingStreamManager{
		messages: make(map[string]int),
		pingCh:   make(chan string, 100),
	}

	// Create tracker2 FIRST so it's ready to receive registry events.
	tracker2, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node2,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker2: %v", err)
	}
	defer func() { _ = tracker2.Close() }()

	tracker1, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node1,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker1: %v", err)
	}

	// Register toolset on node1.
	if err := tracker1.StartPingLoop(ctx, "failover-toolset"); err != nil {
		t.Fatalf("failed to start ping loop: %v", err)
	}

	// Wait for registration event (tracker2 will sync and create its ticker).
	select {
	case <-registryEvents:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for registration event")
	}

	// Wait for at least 2 pings to ensure the distributed ticker is working.
	for range 2 {
		select {
		case <-mockSM.pingCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for pings before failure")
		}
	}

	initialPings := mockSM.getPingCount("failover-toolset")
	if initialPings < 2 {
		t.Errorf("should have received at least 2 pings before failure, got %d", initialPings)
	}

	// Simulate node 1 crash by closing the node directly (without graceful tracker close).
	// This simulates a real crash where the process dies unexpectedly.
	// Pulse should detect the node failure and failover the ticker to node2.
	_ = node1.Close(ctx)

	// Drain any buffered pings that were sent before the node closed.
	pingCountBefore := mockSM.getPingCount("failover-toolset")
drainLoop:
	for {
		select {
		case <-mockSM.pingCh:
			// Drain buffered pings.
		default:
			break drainLoop
		}
	}

	// Wait for NEW pings from node 2 (failover).
	// The distributed ticker should automatically failover to node 2.
	// Give it a bit more time since Pulse needs to detect the node failure.
	for range 3 {
		select {
		case <-mockSM.pingCh:
		case <-time.After(10 * time.Second):
			t.Fatalf("timeout waiting for pings after failover (before=%d, current=%d)",
				pingCountBefore, mockSM.getPingCount("failover-toolset"))
		}
	}

	finalPings := mockSM.getPingCount("failover-toolset")
	if finalPings <= pingCountBefore {
		t.Errorf("pings should continue after node failure: before=%d, after=%d", pingCountBefore, finalPings)
	}
}

// TestPingsContinueAfterPeerTrackerClose verifies that gracefully closing a
// health tracker on one node does not stop the shared distributed ticker for
// other nodes.
//
// Regression: HealthTracker.Close used to call (*pool.Ticker).Stop, which deletes
// the shared ticker-map entry and can stop pings cluster-wide during rolling
// updates.
func TestPingsContinueAfterPeerTrackerClose(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	poolName := "pool-" + t.Name()
	node1, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	defer func() { _ = node1.Close(ctx) }()

	node2, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node2: %v", err)
	}
	defer func() { _ = node2.Close(ctx) }()

	mockSM := &pingCountingStreamManager{
		messages: make(map[string]int),
		pingCh:   make(chan string, 100),
	}

	// Create tracker2 FIRST so it's ready to receive registry events.
	tracker2, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node2,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker2: %v", err)
	}
	defer func() { _ = tracker2.Close() }()

	tracker1, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node1,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		t.Fatalf("failed to create tracker1: %v", err)
	}

	toolset := "peer-close-toolset"
	if err := tracker1.StartPingLoop(ctx, toolset); err != nil {
		_ = tracker1.Close()
		t.Fatalf("failed to start ping loop: %v", err)
	}

	// Wait for registration event (tracker2 will sync and create its ticker).
	select {
	case <-registryEvents:
	case <-time.After(5 * time.Second):
		_ = tracker1.Close()
		t.Fatal("timeout waiting for registration event")
	}

	// Deterministically ensure tracker2 processed the registry entry and started
	// its local distributed ticker participation before we close tracker1.
	//
	// The tracker receives registry events asynchronously, and observing a map
	// event from this test does not guarantee tracker2 has already synced. Avoid
	// forcing a sync here: wait for the local ticker to appear to exercise the
	// real async event path.
	ht2, ok := tracker2.(*healthTracker)
	if !ok {
		_ = tracker1.Close()
		t.Fatalf("unexpected tracker2 type %T", tracker2)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		ht2.mu.Lock()
		_, hasTicker := ht2.tickers[toolset]
		ht2.mu.Unlock()
		if hasTicker {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			_ = tracker1.Close()
			t.Fatalf("tracker2 did not start local ticker for %q", toolset)
		}
	}

	// Wait for at least 2 pings to ensure the distributed ticker is working.
	for range 2 {
		select {
		case <-mockSM.pingCh:
		case <-time.After(5 * time.Second):
			_ = tracker1.Close()
			t.Fatal("timeout waiting for pings before peer close")
		}
	}

	pingCountBefore := mockSM.getPingCount(toolset)

	// Gracefully close tracker1 (simulating rolling update shutdown).
	if err := tracker1.Close(); err != nil {
		t.Fatalf("failed to close tracker1: %v", err)
	}

	// Pings should continue from the remaining node.
	for range 3 {
		select {
		case <-mockSM.pingCh:
		case <-time.After(10 * time.Second):
			t.Fatalf(
				"timeout waiting for pings after peer close (before=%d, current=%d)",
				pingCountBefore,
				mockSM.getPingCount(toolset),
			)
		}
	}
	if mockSM.getPingCount(toolset) <= pingCountBefore {
		t.Fatalf(
			"expected ping count to increase after peer close: before=%d after=%d",
			pingCountBefore,
			mockSM.getPingCount(toolset),
		)
	}
}

// pingCountingStreamManager counts pings per toolset and signals via channel.
type pingCountingStreamManager struct {
	mu       sync.RWMutex
	messages map[string]int
	pingCh   chan string
}

func (m *pingCountingStreamManager) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
	return nil, "mock-stream:" + toolset, nil
}

func (m *pingCountingStreamManager) GetStream(toolset string) clientspulse.Stream {
	return nil
}

func (m *pingCountingStreamManager) RemoveStream(toolset string) {}

func (m *pingCountingStreamManager) PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error {
	if msg.Type == toolregistry.MessageTypePing {
		m.mu.Lock()
		m.messages[toolset]++
		m.mu.Unlock()
		select {
		case m.pingCh <- toolset:
		default:
		}
	}
	return nil
}

func (m *pingCountingStreamManager) getPingCount(toolset string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.messages[toolset]
}

var _ StreamManager = (*pingCountingStreamManager)(nil)
