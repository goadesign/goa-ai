package registry

import (
	"context"
	"testing"
	"time"

	"goa.design/pulse/pool"
	"goa.design/pulse/rmap"
)

// TestPingsSurviveReregisterAndFailover verifies that calling StartPingLoop again
// (simulating a provider re-register) does not break distributed ticker failover.
//
// Regression: StartPingLoop used to stop and restart the local distributed ticker,
// which deletes the shared ticker-map entry and remotely stops other nodes'
// tickers. If the registering node then crashes, no node continues pinging.
func TestPingsSurviveReregisterAndFailover(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-reregister-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-reregister-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	poolName := "pool-" + t.Name()
	node1, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		t.Fatalf("failed to create node1: %v", err)
	}
	node2, err := pool.AddNode(ctx, poolName, rdb, testNodeOpts()...)
	if err != nil {
		_ = node1.Close(ctx)
		t.Fatalf("failed to create node2: %v", err)
	}
	defer func() { _ = node2.Close(ctx) }()

	mockSM := &pingCountingStreamManager{
		messages: make(map[string]int),
		pingCh:   make(chan string, 100),
	}

	tracker2, err := NewHealthTracker(
		mockSM,
		healthMap,
		registryMap,
		node2,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
	)
	if err != nil {
		_ = node1.Close(ctx)
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
		_ = node1.Close(ctx)
		t.Fatalf("failed to create tracker1: %v", err)
	}

	toolset := "reregister-toolset"
	if err := tracker1.StartPingLoop(ctx, toolset); err != nil {
		_ = tracker1.Close()
		_ = node1.Close(ctx)
		t.Fatalf("failed to start ping loop: %v", err)
	}

	// Wait for pings.
	for range 3 {
		select {
		case <-mockSM.pingCh:
		case <-time.After(10 * time.Second):
			_ = tracker1.Close()
			_ = node1.Close(ctx)
			t.Fatal("timeout waiting for pings before re-register")
		}
	}

	// Simulate provider re-register hitting the same gateway node.
	if err := tracker1.StartPingLoop(ctx, toolset); err != nil {
		_ = tracker1.Close()
		_ = node1.Close(ctx)
		t.Fatalf("failed to re-start ping loop: %v", err)
	}

	// Crash node1 without closing tracker1 (simulates process death).
	_ = node1.Close(ctx)

	// Drain buffered pings.
drain:
	for {
		select {
		case <-mockSM.pingCh:
		default:
			break drain
		}
	}

	pingCountBefore := mockSM.getPingCount(toolset)

	// Pings should continue from the remaining node.
	for range 3 {
		select {
		case <-mockSM.pingCh:
		case <-time.After(10 * time.Second):
			t.Fatalf(
				"timeout waiting for pings after failover (before=%d, current=%d)",
				pingCountBefore,
				mockSM.getPingCount(toolset),
			)
		}
	}
	if mockSM.getPingCount(toolset) <= pingCountBefore {
		t.Fatalf("expected ping count to increase after failover: before=%d after=%d", pingCountBefore, mockSM.getPingCount(toolset))
	}
}
