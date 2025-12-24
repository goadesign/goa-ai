package registry

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestMemoryCacheGetSetDelete tests basic cache operations.
// **Validates: Requirements 8.1**
func TestMemoryCacheGetSetDelete(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()

	// Test Set and Get
	schema := &ToolsetSchema{
		ID:          "test-id",
		Name:        "test-toolset",
		Description: "A test toolset",
		Version:     "1.0.0",
		Tools: []*ToolSchema{
			{Name: "tool1", Description: "Tool 1", PayloadSchema: []byte(`{"type":"object"}`)},
		},
	}

	err := cache.Set(ctx, "key1", schema, time.Hour)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for existing key")
	}
	if got.ID != schema.ID {
		t.Errorf("Get returned wrong ID: got %q, want %q", got.ID, schema.ID)
	}
	if got.Name != schema.Name {
		t.Errorf("Get returned wrong Name: got %q, want %q", got.Name, schema.Name)
	}

	// Test Get for non-existent key
	got, err = cache.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get for nonexistent key failed: %v", err)
	}
	if got != nil {
		t.Error("Get returned non-nil for nonexistent key")
	}

	// Test Delete
	err = cache.Delete(ctx, "key1")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	got, err = cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get after Delete failed: %v", err)
	}
	if got != nil {
		t.Error("Get returned non-nil after Delete")
	}
}

// TestMemoryCacheTTLExpiration tests that entries expire after TTL.
// **Validates: Requirements 8.1, 8.3**
func TestMemoryCacheTTLExpiration(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()

	schema := &ToolsetSchema{
		ID:   "expiring-id",
		Name: "expiring-toolset",
	}

	// Set with very short TTL
	err := cache.Set(ctx, "expiring-key", schema, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Should be available immediately
	got, err := cache.Get(ctx, "expiring-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil before TTL expiration")
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Should be expired now
	got, err = cache.Get(ctx, "expiring-key")
	if err != nil {
		t.Fatalf("Get after expiration failed: %v", err)
	}
	if got != nil {
		t.Error("Get returned non-nil after TTL expiration")
	}
}

// TestMemoryCacheClear tests the Clear method.
// **Validates: Requirements 8.1**
func TestMemoryCacheClear(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()

	// Add multiple entries
	for i := range 5 {
		schema := &ToolsetSchema{
			ID:   string(rune('a' + i)),
			Name: string(rune('a' + i)),
		}
		if err := cache.Set(ctx, schema.ID, schema, time.Hour); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}

	if cache.Len() != 5 {
		t.Errorf("Len before Clear: got %d, want 5", cache.Len())
	}

	cache.Clear()

	if cache.Len() != 0 {
		t.Errorf("Len after Clear: got %d, want 0", cache.Len())
	}
}

// TestMemoryCacheLen tests the Len method.
// **Validates: Requirements 8.1**
func TestMemoryCacheLen(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()

	if cache.Len() != 0 {
		t.Errorf("Len of empty cache: got %d, want 0", cache.Len())
	}

	schema := &ToolsetSchema{ID: "test", Name: "test"}
	if err := cache.Set(ctx, "key1", schema, time.Hour); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	if cache.Len() != 1 {
		t.Errorf("Len after one Set: got %d, want 1", cache.Len())
	}

	if err := cache.Set(ctx, "key2", schema, time.Hour); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	if cache.Len() != 2 {
		t.Errorf("Len after two Sets: got %d, want 2", cache.Len())
	}
}

// TestMemoryCacheOverwrite tests that Set overwrites existing entries.
// **Validates: Requirements 8.1**
func TestMemoryCacheOverwrite(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()

	schema1 := &ToolsetSchema{ID: "id1", Name: "name1", Version: "1.0"}
	schema2 := &ToolsetSchema{ID: "id2", Name: "name2", Version: "2.0"}

	if err := cache.Set(ctx, "key", schema1, time.Hour); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, _ := cache.Get(ctx, "key")
	if got.Version != "1.0" {
		t.Errorf("Version before overwrite: got %q, want %q", got.Version, "1.0")
	}

	if err := cache.Set(ctx, "key", schema2, time.Hour); err != nil {
		t.Fatalf("Set (overwrite) failed: %v", err)
	}

	got, _ = cache.Get(ctx, "key")
	if got.Version != "2.0" {
		t.Errorf("Version after overwrite: got %q, want %q", got.Version, "2.0")
	}

	if cache.Len() != 1 {
		t.Errorf("Len after overwrite: got %d, want 1", cache.Len())
	}
}

// TestMemoryCacheConcurrency tests concurrent access to the cache.
// **Validates: Requirements 8.1**
func TestMemoryCacheConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := NewMemoryCache()

	var wg sync.WaitGroup
	numGoroutines := 10
	numOperations := 100

	// Concurrent writes
	for i := range numGoroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range numOperations {
				schema := &ToolsetSchema{
					ID:   "concurrent",
					Name: "concurrent",
				}
				key := string(rune('a' + (id+j)%26))
				_ = cache.Set(ctx, key, schema, time.Hour)
			}
		}(i)
	}

	// Concurrent reads
	for range numGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range numOperations {
				key := string(rune('a' + j%26))
				_, _ = cache.Get(ctx, key)
			}
		}()
	}

	wg.Wait()
	// Test passes if no race conditions or panics occur
}

// TestMemoryCacheDeleteNonExistent tests deleting a non-existent key.
// **Validates: Requirements 8.1**
func TestMemoryCacheDeleteNonExistent(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache()

	// Delete non-existent key should not error
	err := cache.Delete(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Delete of nonexistent key returned error: %v", err)
	}
}

// TestMemoryCacheBackgroundRefresh tests the background refresh functionality.
// **Validates: Requirements 8.3**
func TestMemoryCacheBackgroundRefresh(t *testing.T) {
	ctx := context.Background()

	refreshCalled := make(chan string, 10)
	refreshFunc := func(_ context.Context, key string) (*ToolsetSchema, error) {
		refreshCalled <- key
		return &ToolsetSchema{
			ID:      "refreshed-" + key,
			Name:    "refreshed",
			Version: "2.0",
		}, nil
	}

	cache := NewMemoryCache(
		WithRefreshFunc(refreshFunc),
		WithRefreshCooldown(10*time.Millisecond),
	)

	// Start refresh loop
	cache.StartRefresh(ctx)
	defer cache.StopRefresh()

	// Set entry with short TTL
	schema := &ToolsetSchema{ID: "original", Name: "original", Version: "1.0"}
	if err := cache.Set(ctx, "refresh-key", schema, 100*time.Millisecond); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Wait until we're within the refresh threshold (20% of TTL = 20ms from expiry)
	time.Sleep(90 * time.Millisecond)

	// Trigger refresh by accessing the entry
	_, _ = cache.Get(ctx, "refresh-key")

	// Wait for refresh to complete
	select {
	case key := <-refreshCalled:
		if key != "refresh-key" {
			t.Errorf("Refresh called with wrong key: got %q, want %q", key, "refresh-key")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("Refresh was not triggered within timeout")
	}

	// Give time for the refresh to update the cache
	time.Sleep(50 * time.Millisecond)

	// Verify the entry was refreshed
	got, _ := cache.Get(ctx, "refresh-key")
	if got == nil {
		t.Fatal("Get returned nil after refresh")
	}
	if got.Version != "2.0" {
		t.Errorf("Version after refresh: got %q, want %q", got.Version, "2.0")
	}
}

// TestMemoryCacheRefreshCooldown tests that refresh respects cooldown period.
// **Validates: Requirements 8.3**
func TestMemoryCacheRefreshCooldown(t *testing.T) {
	ctx := context.Background()

	refreshCount := 0
	var mu sync.Mutex
	refreshFunc := func(_ context.Context, _ string) (*ToolsetSchema, error) {
		mu.Lock()
		refreshCount++
		mu.Unlock()
		return &ToolsetSchema{ID: "refreshed", Name: "refreshed"}, nil
	}

	cache := NewMemoryCache(
		WithRefreshFunc(refreshFunc),
		WithRefreshCooldown(200*time.Millisecond),
	)

	cache.StartRefresh(ctx)
	defer cache.StopRefresh()

	// Set entry with TTL that will trigger refresh
	schema := &ToolsetSchema{ID: "original", Name: "original"}
	if err := cache.Set(ctx, "cooldown-key", schema, 50*time.Millisecond); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Wait until within refresh threshold
	time.Sleep(45 * time.Millisecond)

	// Multiple Gets should only trigger one refresh due to cooldown
	for range 5 {
		_, _ = cache.Get(ctx, "cooldown-key")
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for any pending refreshes
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := refreshCount
	mu.Unlock()

	// Should have at most 1 refresh due to cooldown
	if count > 1 {
		t.Errorf("Refresh called %d times, expected at most 1 due to cooldown", count)
	}
}

// TestMemoryCacheRefreshNotStarted tests that refresh doesn't trigger when not started.
// **Validates: Requirements 8.3**
func TestMemoryCacheRefreshNotStarted(t *testing.T) {
	ctx := context.Background()

	refreshCalled := false
	refreshFunc := func(_ context.Context, _ string) (*ToolsetSchema, error) {
		refreshCalled = true
		return &ToolsetSchema{ID: "refreshed", Name: "refreshed"}, nil
	}

	cache := NewMemoryCache(
		WithRefreshFunc(refreshFunc),
	)
	// Note: NOT calling StartRefresh

	schema := &ToolsetSchema{ID: "original", Name: "original"}
	if err := cache.Set(ctx, "no-refresh-key", schema, 50*time.Millisecond); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Wait until within refresh threshold
	time.Sleep(45 * time.Millisecond)

	// Get should not trigger refresh since loop is not started
	_, _ = cache.Get(ctx, "no-refresh-key")

	time.Sleep(50 * time.Millisecond)

	if refreshCalled {
		t.Error("Refresh was called even though StartRefresh was not called")
	}
}

// TestNoopCacheImplementsInterface tests that noopCache implements Cache interface.
// **Validates: Requirements 8.1**
func TestNoopCacheImplementsInterface(t *testing.T) {
	var _ Cache = &noopCache{}

	ctx := context.Background()
	cache := &noopCache{}

	// Get always returns nil, nil
	got, err := cache.Get(ctx, "any-key")
	if err != nil {
		t.Errorf("noopCache.Get returned error: %v", err)
	}
	if got != nil {
		t.Error("noopCache.Get returned non-nil")
	}

	// Set always succeeds
	err = cache.Set(ctx, "any-key", &ToolsetSchema{}, time.Hour)
	if err != nil {
		t.Errorf("noopCache.Set returned error: %v", err)
	}

	// Delete always succeeds
	err = cache.Delete(ctx, "any-key")
	if err != nil {
		t.Errorf("noopCache.Delete returned error: %v", err)
	}
}
