package registry

import (
	"context"
	"testing"
	"time"

	"goa.design/goa-ai/registry/store/memory"
	"goa.design/pulse/pool"
)

// TestNewRegistry verifies that the Registry constructor wires all components correctly.
func TestNewRegistry(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Create registry with default config.
	reg, err := New(ctx, Config{
		Redis:               rdb,
		Name:                "test-" + t.Name(),
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
		PoolNodeOptions:     testNodeOpts(),
	})
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}
	defer func() {
		if err := reg.Close(ctx); err != nil {
			t.Errorf("failed to close registry: %v", err)
		}
	}()

	// Verify service is accessible.
	if reg.Service() == nil {
		t.Error("Service() should return non-nil service")
	}
}

// TestNewRegistryWithCustomStore verifies that a custom store can be injected.
func TestNewRegistryWithCustomStore(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Create a custom store.
	customStore := memory.New()

	reg, err := New(ctx, Config{
		Redis:               rdb,
		Store:               customStore,
		Name:                "test-" + t.Name(),
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
		PoolNodeOptions:     testNodeOpts(),
	})
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}
	defer func() {
		if err := reg.Close(ctx); err != nil {
			t.Errorf("failed to close registry: %v", err)
		}
	}()

	// Verify service is accessible.
	if reg.Service() == nil {
		t.Error("Service() should return non-nil service")
	}
}

// TestNewRegistryRequiresRedis verifies that Redis client is required.
func TestNewRegistryRequiresRedis(t *testing.T) {
	ctx := context.Background()

	_, err := New(ctx, Config{})
	if err == nil {
		t.Error("expected error when Redis is nil")
	}
}

// TestRegistryGracefulShutdown verifies that Close properly cleans up resources.
func TestRegistryGracefulShutdown(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	reg, err := New(ctx, Config{
		Redis:               rdb,
		Name:                "test-" + t.Name(),
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
		PoolNodeOptions: []pool.NodeOption{
			pool.WithJobSinkBlockDuration(100 * time.Millisecond),
		},
	})
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	// Close should complete without error.
	if err := reg.Close(ctx); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// Calling Close again should be safe (idempotent health tracker close).
	// Note: Other components may error on double-close, but that's expected.
}
