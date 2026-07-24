//go:build integration

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
	"goa.design/pulse/rmap"
)

var (
	testRedisClient    *redis.Client
	testRedisContainer testcontainers.Container
)

// TestMain provisions the one Redis container the integration-tagged suite
// runs against. The `integration` build tag is an explicit opt-in to
// Docker-backed tests, so failure to provision Redis fails the run loudly —
// a skip here would let a broken runner turn the whole lane green with zero
// tests executed. Developers without Docker simply do not pass the tag.
func TestMain(m *testing.M) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections"),
	}
	var err error
	testRedisContainer, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		fmt.Printf("integration tests require Docker: failed to start redis container: %v\n", err)
		os.Exit(1)
	}
	host, err := testRedisContainer.Host(ctx)
	if err != nil {
		fmt.Printf("integration tests require Docker: failed to get container host: %v\n", err)
		os.Exit(1)
	}
	port, err := testRedisContainer.MappedPort(ctx, "6379")
	if err != nil {
		fmt.Printf("integration tests require Docker: failed to get container port: %v\n", err)
		os.Exit(1)
	}
	testRedisClient = redis.NewClient(&redis.Options{
		Addr: host + ":" + port.Port(),
	})
	if err := testRedisClient.Ping(ctx).Err(); err != nil {
		fmt.Printf("integration tests require Docker: failed to ping redis: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := testRedisClient.Close(); err != nil {
		fmt.Printf("failed to close redis client: %v\n", err)
	}
	if err := testRedisContainer.Terminate(ctx); err != nil {
		fmt.Printf("failed to terminate redis container: %v\n", err)
	}

	os.Exit(code)
}

// getRedis returns the shared Redis client and flushes the database for test isolation.
func getRedis(t *testing.T) *redis.Client {
	t.Helper()
	if err := testRedisClient.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("failed to flush redis: %v", err)
	}
	return testRedisClient
}

// TestMultiNodeRegistrationSync verifies that when a toolset is registered on
// one node, the whole cluster starts pinging it (exactly one node per interval
// wins the ping lease) while the toolset stays unhealthy until a provider pong.
func TestMultiNodeRegistrationSync(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Use unique map names per test to ensure isolation.
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

	mockSM := newPingChannelStreamManager()

	// Create two health trackers (simulating two gateway nodes).
	tracker1 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)
	tracker2 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	// Register a toolset on node 1.
	registerCatalogToolset(t, ctx, registryMap, tracker1, "test-toolset")

	// The cluster (either node) must start pinging the registered toolset.
	waitForPing(t, mockSM, "test-toolset")

	// A toolset is healthy only after a provider Pong; StartPingLoop must not
	// treat registration as provider liveness.
	if tracker1.IsHealthy("test-toolset") {
		t.Error("tracker1 should see toolset as unhealthy before any pong")
	}
	if tracker2.IsHealthy("test-toolset") {
		t.Error("tracker2 should see toolset as unhealthy before any pong")
	}

	// Verify the toolset is in the registry map.
	_, ok := registryMap.Get(toolsetCatalogKey("test-toolset"))
	if !ok {
		t.Error("toolset should be in registry map")
	}
}

// TestCatalogRegistrationStartsPings verifies that catalog membership alone
// drives ping scheduling: saving a catalog entry starts pings without any
// StartPingLoop call.
func TestCatalogRegistrationStartsPings(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-catalog-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-catalog-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	mockSM := newPingChannelStreamManager()
	tracker := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	catalog := newToolsetCatalog(registryMap)
	toolset := "catalog-toolset"
	if err := catalog.SaveToolset(ctx, testCatalogToolset(toolset, "Catalog registration", []string{"catalog"})); err != nil {
		t.Fatalf("failed to save catalog toolset: %v", err)
	}

	waitForPing(t, mockSM, toolset)

	if tracker.IsHealthy(toolset) {
		t.Error("tracker should see toolset as unhealthy before any pong")
	}
}

// TestMultiNodeUnregistrationSync verifies that when a toolset is unregistered
// on one node, pings stop cluster-wide and shared health state is cleaned up.
func TestMultiNodeUnregistrationSync(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

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

	mockSM := newPingChannelStreamManager()
	tracker1 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)
	tracker2 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	// Register on node 1 and wait for cluster pings.
	registerCatalogToolset(t, ctx, registryMap, tracker1, "test-toolset")
	waitForPing(t, mockSM, "test-toolset")

	// Seed shared health state for the active registration so unregister cleanup
	// has real distributed state to delete.
	catalog := newToolsetCatalog(registryMap)
	registrationToken, err := catalog.RegistrationToken(ctx, "test-toolset")
	if err != nil {
		t.Fatalf("failed to resolve registration token: %v", err)
	}
	if err := setHealthRecordForTest(ctx, healthMap, "test-toolset", "provider-a", registrationToken, time.Now()); err != nil {
		t.Fatalf("failed to seed health record: %v", err)
	}

	// Unregister on node 2 (different node than registration).
	unregisterCatalogToolset(t, ctx, registryMap, tracker2, "test-toolset")

	// Toolset should be removed from registry map.
	waitForMapKeyRemoval(t, registryMap, toolsetCatalogKey("test-toolset"))

	// Health state should be cleaned up.
	waitForMapKeyRemoval(t, healthMap, healthKey("test-toolset", "provider-a"))
}

// TestNewNodeBootstrapsExistingCatalogToolsets verifies that a tracker created
// after a toolset was cataloged (a new gateway node joining, or a node
// restarting) participates in ping scheduling without any registration event.
func TestNewNodeBootstrapsExistingCatalogToolsets(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-bootstrap-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-bootstrap-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	catalog := newToolsetCatalog(registryMap)
	toolset := "bootstrap-toolset"
	if err := catalog.SaveToolset(ctx, testCatalogToolset(toolset, "Bootstrap catalog entry", []string{"bootstrap"})); err != nil {
		t.Fatalf("failed to save catalog toolset: %v", err)
	}

	mockSM := newPingChannelStreamManager()
	tracker := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	waitForPing(t, mockSM, toolset)
	if tracker.IsHealthy(toolset) {
		t.Error("tracker should see existing catalog toolset as unhealthy before any pong")
	}
}

// TestPingsContinueAfterPeerClose verifies that pings continue when the node
// currently winning the ping lease goes away. With expiring leases there is no
// distinction between graceful shutdown and crash: the departed node simply
// stops contending, its last lease expires, and a surviving node takes over.
func TestPingsContinueAfterPeerClose(t *testing.T) {
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

	mockSM := newPingChannelStreamManager()
	tracker1 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)
	tracker2 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	toolset := "failover-toolset"
	registerCatalogToolset(t, ctx, registryMap, tracker1, toolset)

	// Wait for at least 2 pings to ensure scheduling is working.
	for range 2 {
		waitForPing(t, mockSM, toolset)
	}
	if tracker2.IsHealthy(toolset) {
		t.Error("tracker2 should see toolset as unhealthy before any pong")
	}

	// Close tracker1 (rolling update or crash; leases make them equivalent).
	if err := tracker1.Close(); err != nil {
		t.Fatalf("failed to close tracker1: %v", err)
	}
	mockSM.drain()
	pingCountBefore := mockSM.getPingCount(toolset)

	// Pings must continue from the remaining node.
	for range 3 {
		waitForPing(t, mockSM, toolset)
	}
	if mockSM.getPingCount(toolset) <= pingCountBefore {
		t.Fatalf(
			"expected ping count to increase after peer close: before=%d after=%d",
			pingCountBefore,
			mockSM.getPingCount(toolset),
		)
	}
}

// TestConcurrentRevisionPinsConverge locks the atomicity of the revision
// repair: pins from many concurrent repairers must converge on the highest
// target instead of summing increments, which would lift the counter far
// above the wall clock and silently break clock domination at the next Redis
// state loss.
func TestConcurrentRevisionPinsConverge(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	hashKey := "map:pin-converge:content"
	defer rdb.Del(ctx, hashKey)

	base := time.Now().UnixMilli()
	const racers = 8
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(target int64) {
			defer wg.Done()
			if err := revisionPinScript.Run(ctx, rdb, []string{hashKey}, target).Err(); err != nil {
				t.Errorf("concurrent pin failed: %v", err)
			}
		}(base + int64(i))
	}
	wg.Wait()

	rev, err := rdb.HGet(ctx, hashKey, "=rev").Int64()
	if err != nil {
		t.Fatalf("failed to read pinned revision: %v", err)
	}
	if want := base + racers - 1; rev != want {
		t.Fatalf("expected converged revision %d, got %d", want, rev)
	}
}

// TestPingsAndHealthRecoverAfterRedisStateLoss reproduces the production
// incident: Redis loses every key (FLUSHDB) while the registry keeps running.
// Ping scheduling must resume on its own — the lease is recreated on the next
// tick — and a provider pong must mark the toolset healthy again instead of
// leaving it permanently unhealthy.
func TestPingsAndHealthRecoverAfterRedisStateLoss(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-recover-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-recover-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	mockSM := newPingChannelStreamManager()
	tracker := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	toolset := "recover-toolset"
	registerCatalogToolset(t, ctx, registryMap, tracker, toolset)
	waitForPing(t, mockSM, toolset)

	// A provider pong marks the toolset healthy. Replicated-map writes become
	// visible to local reads asynchronously, so poll instead of asserting
	// immediately.
	pingID := lastPingID(t, mockSM, toolset)
	if err := tracker.RecordPong(ctx, toolset, "provider-a", pingID); err != nil {
		t.Fatalf("failed to record pong: %v", err)
	}
	waitForHealthy(t, tracker, toolset)

	// Inflate the health map revision so the flush resets Redis far below the
	// local replica revision. Without revision repair, every post-flush write
	// would be silently dropped by the replica and this test fails
	// deterministically instead of only under unlucky timing.
	for range 20 {
		if _, err := healthMap.Set(ctx, "scratch", "x"); err != nil {
			t.Fatalf("failed to inflate health map revision: %v", err)
		}
	}
	if _, err := healthMap.Delete(ctx, "scratch"); err != nil {
		t.Fatalf("failed to remove scratch key: %v", err)
	}

	// Redis loses all state (consumer groups, leases, map hashes).
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("failed to flush redis: %v", err)
	}
	mockSM.drain()

	// Ping scheduling must self-heal: the next tick recreates the lease.
	for range 3 {
		waitForPing(t, mockSM, toolset)
	}

	// The pre-flush pong ages out and the toolset goes unhealthy, exactly as in
	// the incident. Recovery below can then only come from a post-flush pong
	// actually propagating.
	waitForUnhealthy(t, tracker, toolset)

	// A provider answering a post-flush ping must restore health.
	waitForPing(t, mockSM, toolset)
	pingID = lastPingID(t, mockSM, toolset)
	if err := tracker.RecordPong(ctx, toolset, "provider-a", pingID); err != nil {
		t.Fatalf("failed to record pong after flush: %v", err)
	}
	waitForHealthy(t, tracker, toolset)
}

// newTestTracker builds a health tracker with fast test intervals and
// registers cleanup.
func newTestTracker(t *testing.T, sm StreamManager, healthMap, registryMap *rmap.Map, rdb *redis.Client) HealthTracker {
	t.Helper()

	tracker, err := NewHealthTracker(
		sm,
		healthMap,
		registryMap,
		rdb,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
		WithPingLeaseScope("lease-"+t.Name()),
	)
	if err != nil {
		t.Fatalf("failed to create tracker: %v", err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	return tracker
}

// waitForHealthy polls until the tracker reports the toolset healthy.
// Replicated-map writes propagate to local replicas asynchronously, so health
// reads after a pong must poll rather than assert immediately.
func waitForHealthy(t *testing.T, tracker HealthTracker, toolset string) {
	t.Helper()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if tracker.IsHealthy(toolset) {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("toolset %q did not become healthy", toolset)
		}
	}
}

// waitForUnhealthy polls until the tracker reports the toolset unhealthy,
// i.e. the last recorded pong has aged past the staleness threshold.
func waitForUnhealthy(t *testing.T, tracker HealthTracker, toolset string) {
	t.Helper()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if !tracker.IsHealthy(toolset) {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("toolset %q did not become unhealthy", toolset)
		}
	}
}

// waitForPing blocks until a ping for the toolset is published or fails the test.
func waitForPing(t *testing.T, sm *pingChannelStreamManager, toolset string) {
	t.Helper()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-sm.pingCh:
			if got == toolset {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for ping for %q", toolset)
		}
	}
}

func waitForMapKeyRemoval(t *testing.T, rm *rmap.Map, key string) {
	t.Helper()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, ok := rm.Get(key); !ok {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("map key %q was not removed", key)
		}
	}
}

func registerCatalogToolset(t *testing.T, ctx context.Context, registryMap *rmap.Map, tracker HealthTracker, toolset string) {
	t.Helper()

	catalog := newToolsetCatalog(registryMap)
	if err := catalog.SaveToolset(ctx, testCatalogToolset(toolset, "Health tracker test toolset", []string{"health"})); err != nil {
		t.Fatalf("failed to save catalog toolset: %v", err)
	}
	if err := tracker.StartPingLoop(ctx, toolset); err != nil {
		t.Fatalf("failed to start ping loop: %v", err)
	}
}

func unregisterCatalogToolset(t *testing.T, ctx context.Context, registryMap *rmap.Map, tracker HealthTracker, toolset string) {
	t.Helper()

	catalog := newToolsetCatalog(registryMap)
	if err := catalog.DeleteToolset(ctx, toolset); err != nil {
		t.Fatalf("failed to delete catalog toolset: %v", err)
	}
	tracker.StopPingLoop(ctx, toolset)
}

// lastPingID returns the ping ID of the most recent ping recorded for a toolset.
func lastPingID(t *testing.T, sm *pingChannelStreamManager, toolset string) string {
	t.Helper()

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	id := sm.lastPingIDs[toolset]
	if id == "" {
		t.Fatalf("no ping recorded for %q", toolset)
	}
	return id
}

// pingChannelStreamManager counts pings per toolset, remembers the last ping
// ID, and signals each ping via channel.
type pingChannelStreamManager struct {
	mu          sync.RWMutex
	messages    map[string]int
	lastPingIDs map[string]string
	pingCh      chan string
}

func newPingChannelStreamManager() *pingChannelStreamManager {
	return &pingChannelStreamManager{
		messages:    make(map[string]int),
		lastPingIDs: make(map[string]string),
		pingCh:      make(chan string, 1024),
	}
}

func (m *pingChannelStreamManager) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
	return nil, "mock-stream:" + toolset, nil
}

func (m *pingChannelStreamManager) GetStream(toolset string) clientspulse.Stream {
	return nil
}

func (m *pingChannelStreamManager) RemoveStream(toolset string) {}

func (m *pingChannelStreamManager) PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error {
	if msg.Type == toolregistry.MessageTypePing {
		m.mu.Lock()
		m.messages[toolset]++
		m.lastPingIDs[toolset] = msg.PingID
		m.mu.Unlock()
		select {
		case m.pingCh <- toolset:
		default:
		}
	}
	return nil
}

func (m *pingChannelStreamManager) getPingCount(toolset string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.messages[toolset]
}

// drain discards buffered ping notifications so subsequent waits observe only
// new pings.
func (m *pingChannelStreamManager) drain() {
	for {
		select {
		case <-m.pingCh:
		default:
			return
		}
	}
}

var _ StreamManager = (*pingChannelStreamManager)(nil)
