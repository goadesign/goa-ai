//go:build integration

package registry

import (
	"context"
	"testing"
	"time"

	"goa.design/pulse/rmap"
)

// TestPingsSurviveReregisterAndFailover verifies that calling StartPingLoop
// again (simulating a provider re-register) does not disturb ping scheduling,
// and that pings continue when the node that handled the re-register goes
// away.
//
// Regression: StartPingLoop used to stop and restart a Pulse distributed
// ticker, which deleted shared ticker state and could remotely stop other
// nodes' tickers; a subsequent crash of the registering node left no node
// pinging. StartPingLoop is now ensure-only and scheduling is derived from the
// catalog plus an expiring Redis lease, so re-registration has nothing to
// restart.
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

	mockSM := newPingChannelStreamManager()
	tracker1 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)
	tracker2 := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	toolset := "reregister-toolset"
	registerCatalogToolset(t, ctx, registryMap, tracker1, toolset)

	// Wait for pings.
	for range 3 {
		waitForPing(t, mockSM, toolset)
	}

	// Simulate provider re-register hitting the same gateway node.
	if err := tracker1.StartPingLoop(ctx, toolset); err != nil {
		t.Fatalf("failed to re-start ping loop: %v", err)
	}

	// The re-registering node goes away (crash or rolling update).
	if err := tracker1.Close(); err != nil {
		t.Fatalf("failed to close tracker1: %v", err)
	}
	mockSM.drain()
	pingCountBefore := mockSM.getPingCount(toolset)

	// Pings should continue from the remaining node.
	for range 3 {
		waitForPing(t, mockSM, toolset)
	}
	if mockSM.getPingCount(toolset) <= pingCountBefore {
		t.Fatalf("expected ping count to increase after failover: before=%d after=%d", pingCountBefore, mockSM.getPingCount(toolset))
	}
	if tracker2.IsHealthy(toolset) {
		t.Error("tracker2 should see toolset as unhealthy before any pong")
	}
}

// TestStartPingLoopEnsureOnlyDoesNotDuplicatePings verifies that repeated
// StartPingLoop calls (registration renewals, provider re-registrations) never
// multiply the ping rate: the ping lease admits one ping per interval no
// matter how many ensure calls or trackers contend.
func TestStartPingLoopEnsureOnlyDoesNotDuplicatePings(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	healthMap, err := rmap.Join(ctx, "health-ensure-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create health map: %v", err)
	}
	defer healthMap.Close()

	registryMap, err := rmap.Join(ctx, "registry-ensure-"+t.Name(), rdb)
	if err != nil {
		t.Fatalf("failed to create registry map: %v", err)
	}
	defer registryMap.Close()

	mockSM := newPingChannelStreamManager()
	tracker := newTestTracker(t, mockSM, healthMap, registryMap, rdb)

	toolset := "ensure-toolset"
	registerCatalogToolset(t, ctx, registryMap, tracker, toolset)
	waitForPing(t, mockSM, toolset)

	// Hammer the ensure path as aggressive re-registration would.
	for range 10 {
		if err := tracker.StartPingLoop(ctx, toolset); err != nil {
			t.Fatalf("failed to ensure ping loop: %v", err)
		}
	}
	mockSM.drain()

	// Measure the ping rate over 20 intervals (50ms ping interval). The lease
	// admits at most one ping per interval, so even generous jitter bounds stay
	// far below the 10x rate duplicated loops would produce.
	const window = time.Second
	before := mockSM.getPingCount(toolset)
	time.Sleep(window)
	pings := mockSM.getPingCount(toolset) - before
	if pings == 0 {
		t.Fatal("expected pings to continue after ensure calls")
	}
	if pings > 25 {
		t.Fatalf("ping rate suggests duplicated scheduling: got %d pings in %s (interval 50ms)", pings, window)
	}
}
