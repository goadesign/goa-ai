//go:build integration

package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

// TestNewRegistryRejectsIncompatibleCatalog verifies that constructor-time
// validation fails before any service can use a legacy persisted lease shape.
func TestNewRegistryRejectsIncompatibleCatalog(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	name := fmt.Sprintf("poisoned-catalog-%d", time.Now().UnixNano())
	// A combined record from the old format must not be accepted by the new
	// state-only decoder, even when paired definition bytes exist.
	store := newRedisCatalogStore(rdb, name)
	key := toolsetCatalogKey("poison.toolset")
	require.NoError(t, rdb.HSet(ctx, store.state, key, `{"toolset":{"name":"poison.toolset"},"registration_token":"`+testActiveRegistrationToken+`"}`).Err())
	require.NoError(t, rdb.HSet(ctx, store.definitions, key, `{}`).Err())

	reg, err := New(ctx, Config{Redis: rdb, Name: name})

	require.Nil(t, reg)
	require.ErrorContains(t, err, toolsetCatalogKey("poison.toolset"))
	require.ErrorContains(t, err, `unknown field "toolset"`)
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
