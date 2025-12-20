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

	// Subscribe to both registry and health changes before creating trackers.
	registryEvents := registryMap.Subscribe()
	defer registryMap.Unsubscribe(registryEvents)

	healthEvents := healthMap.Subscribe()
	defer healthMap.Unsubscribe(healthEvents)

	// Create two pool nodes simulating two gateway instances.
	node1, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node1", rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	defer func() { _ = node1.Close(ctx) }()

	node2, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node2", rdb, testNodeOpts()...)
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

	// Wait for both registry and health change events.
	// StartPingLoop updates both maps, so we need to wait for both.
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

	// Both nodes should see the toolset as healthy.
	if !tracker1.IsHealthy("test-toolset") {
		t.Error("tracker1 should see toolset as healthy")
	}
	if !tracker2.IsHealthy("test-toolset") {
		t.Error("tracker2 should see toolset as healthy")
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

	node1, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node1", rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	defer func() { _ = node1.Close(ctx) }()

	node2, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node2", rdb, testNodeOpts()...)
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

	node1, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node1", rdb, testNodeOpts()...)
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
	node2, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node2", rdb, testNodeOpts()...)
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

	// New node should immediately see the existing toolset as healthy
	// (synced during NewHealthTracker via syncExistingToolsets).
	if !tracker2.IsHealthy("existing-toolset") {
		t.Error("tracker2 should see existing toolset as healthy after sync")
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

	node1, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node1", rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}

	node2, err := pool.AddNode(ctx, "pool-"+t.Name()+"-node2", rdb, testNodeOpts()...)
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

func (m *pingCountingStreamManager) PublishToolCall(ctx context.Context, toolset string, msg *ToolCallMessage) error {
	if msg.Type == MessageTypePing {
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

//nolint:unparam // toolset parameter kept for API consistency
func (m *pingCountingStreamManager) getPingCount(toolset string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.messages[toolset]
}

var _ StreamManager = (*pingCountingStreamManager)(nil)
