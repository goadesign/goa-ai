package registry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestNewManager tests manager creation with various options.
// **Validates: Requirements 1.3**
func TestNewManager(t *testing.T) {
	t.Run("creates manager with defaults", func(t *testing.T) {
		m := NewManager()
		if m == nil {
			t.Fatal("NewManager returned nil")
		}
		if m.registries == nil {
			t.Error("registries map is nil")
		}
		if m.cache == nil {
			t.Error("cache is nil")
		}
		if m.logger == nil {
			t.Error("logger is nil")
		}
		if m.metrics == nil {
			t.Error("metrics is nil")
		}
		if m.tracer == nil {
			t.Error("tracer is nil")
		}
	})

	t.Run("creates manager with custom cache", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))
		if m.cache != cache {
			t.Error("custom cache not set")
		}
	})
}

// TestAddRegistry tests adding registries to the manager.
// **Validates: Requirements 1.3**
func TestAddRegistry(t *testing.T) {
	m := NewManager()
	client := &mockRegistryClient{}

	m.AddRegistry("test-registry", client, RegistryConfig{
		SyncInterval: time.Minute,
		CacheTTL:     time.Hour,
	})

	m.mu.RLock()
	entry, exists := m.registries["test-registry"]
	m.mu.RUnlock()

	if !exists {
		t.Fatal("registry not added")
	}
	if entry.client != client {
		t.Error("wrong client stored")
	}
	if entry.syncInterval != time.Minute {
		t.Errorf("syncInterval: got %v, want %v", entry.syncInterval, time.Minute)
	}
	if entry.cacheTTL != time.Hour {
		t.Errorf("cacheTTL: got %v, want %v", entry.cacheTTL, time.Hour)
	}
}

// TestDiscoverToolset tests toolset discovery functionality.
// **Validates: Requirements 2.1**
func TestDiscoverToolset(t *testing.T) {
	ctx := context.Background()

	t.Run("discovers toolset from registry", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))

		expectedSchema := &ToolsetSchema{
			ID:          "ts-1",
			Name:        "test-toolset",
			Description: "A test toolset",
			Version:     "1.0.0",
			Tools: []*ToolSchema{
				{Name: "tool1", Description: "Tool 1"},
			},
		}

		client := &mockRegistryClientWithFuncs{
			toolsets: []*ToolsetInfo{
				{ID: "ts-1", Name: "test-toolset"},
			},
			getToolsetFunc: func(_ context.Context, name string) (*ToolsetSchema, error) {
				if name == "test-toolset" {
					return expectedSchema, nil
				}
				return nil, errors.New("not found")
			},
		}

		m.AddRegistry("test-registry", client, RegistryConfig{CacheTTL: time.Hour})

		schema, err := m.DiscoverToolset(ctx, "test-registry", "test-toolset")
		if err != nil {
			t.Fatalf("DiscoverToolset failed: %v", err)
		}
		if schema == nil {
			t.Fatal("schema is nil")
		}
		if schema.Name != expectedSchema.Name {
			t.Errorf("Name: got %q, want %q", schema.Name, expectedSchema.Name)
		}
		if schema.Origin != "test-registry" {
			t.Errorf("Origin: got %q, want %q", schema.Origin, "test-registry")
		}
	})

	t.Run("returns error for unknown registry", func(t *testing.T) {
		m := NewManager()

		_, err := m.DiscoverToolset(ctx, "unknown-registry", "test-toolset")
		if err == nil {
			t.Fatal("expected error for unknown registry")
		}
	})

	t.Run("uses cache on subsequent calls", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))

		callCount := 0
		client := &mockRegistryClientWithFuncs{
			getToolsetFunc: func(_ context.Context, _ string) (*ToolsetSchema, error) {
				callCount++
				return &ToolsetSchema{ID: "ts-1", Name: "cached-toolset"}, nil
			},
		}

		m.AddRegistry("test-registry", client, RegistryConfig{CacheTTL: time.Hour})

		// First call - should hit registry
		_, err := m.DiscoverToolset(ctx, "test-registry", "cached-toolset")
		if err != nil {
			t.Fatalf("first DiscoverToolset failed: %v", err)
		}
		if callCount != 1 {
			t.Errorf("expected 1 registry call, got %d", callCount)
		}

		// Second call - should use cache
		_, err = m.DiscoverToolset(ctx, "test-registry", "cached-toolset")
		if err != nil {
			t.Fatalf("second DiscoverToolset failed: %v", err)
		}
		if callCount != 1 {
			t.Errorf("expected 1 registry call (cached), got %d", callCount)
		}
	})

	t.Run("falls back to cache on registry error", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))

		callCount := 0
		client := &mockRegistryClientWithFuncs{
			getToolsetFunc: func(_ context.Context, _ string) (*ToolsetSchema, error) {
				callCount++
				if callCount == 1 {
					return &ToolsetSchema{ID: "ts-1", Name: "fallback-toolset"}, nil
				}
				return nil, errors.New("registry unavailable")
			},
		}

		m.AddRegistry("test-registry", client, RegistryConfig{CacheTTL: time.Hour})

		// First call - populates cache
		_, err := m.DiscoverToolset(ctx, "test-registry", "fallback-toolset")
		if err != nil {
			t.Fatalf("first DiscoverToolset failed: %v", err)
		}

		// Clear cache entry to force registry call
		_ = cache.Delete(ctx, cacheKey("test-registry", "fallback-toolset"))

		// Re-populate cache for fallback test
		_ = cache.Set(ctx, cacheKey("test-registry", "fallback-toolset"),
			&ToolsetSchema{ID: "ts-1", Name: "fallback-toolset"}, time.Hour)

		// Second call - registry fails, should use cache
		schema, err := m.DiscoverToolset(ctx, "test-registry", "fallback-toolset")
		if err != nil {
			t.Fatalf("second DiscoverToolset failed: %v", err)
		}
		if schema == nil {
			t.Fatal("expected cached schema on fallback")
		}
	})
}

// TestSearch tests search functionality across registries.
// **Validates: Requirements 4.1**
func TestSearch(t *testing.T) {
	ctx := context.Background()

	t.Run("searches single registry", func(t *testing.T) {
		m := NewManager()

		client := &mockRegistryClient{
			results: []*SearchResult{
				{ID: "tool-1", Name: "search-tool", Description: "A search tool", RelevanceScore: 0.9},
			},
		}

		m.AddRegistry("test-registry", client, RegistryConfig{})

		results, err := m.Search(ctx, "search")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Name != "search-tool" {
			t.Errorf("Name: got %q, want %q", results[0].Name, "search-tool")
		}
		if results[0].Origin != "test-registry" {
			t.Errorf("Origin: got %q, want %q", results[0].Origin, "test-registry")
		}
	})

	t.Run("merges results from multiple registries", func(t *testing.T) {
		m := NewManager()

		client1 := &mockRegistryClient{
			results: []*SearchResult{
				{ID: "tool-1", Name: "tool-from-reg1"},
			},
		}
		client2 := &mockRegistryClient{
			results: []*SearchResult{
				{ID: "tool-2", Name: "tool-from-reg2"},
			},
		}

		m.AddRegistry("registry-1", client1, RegistryConfig{})
		m.AddRegistry("registry-2", client2, RegistryConfig{})

		results, err := m.Search(ctx, "tool")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}

		// Check both results are present with correct origins
		origins := make(map[string]bool)
		for _, r := range results {
			origins[r.Origin] = true
		}
		if !origins["registry-1"] || !origins["registry-2"] {
			t.Error("results missing expected origins")
		}
	})

	t.Run("returns empty results for no registries", func(t *testing.T) {
		m := NewManager()

		results, err := m.Search(ctx, "anything")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected empty results, got %d", len(results))
		}
	})

	t.Run("returns partial results when some registries fail", func(t *testing.T) {
		m := NewManager()

		client1 := &mockRegistryClientWithFuncs{
			results: []*SearchResult{
				{ID: "tool-1", Name: "working-tool"},
			},
		}
		client2 := &mockRegistryClientWithFuncs{
			searchFunc: func(_ context.Context, _ string) ([]*SearchResult, error) {
				return nil, errors.New("registry unavailable")
			},
		}

		m.AddRegistry("working-registry", client1, RegistryConfig{})
		m.AddRegistry("failing-registry", client2, RegistryConfig{})

		results, err := m.Search(ctx, "tool")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result from working registry, got %d", len(results))
		}
	})

	t.Run("returns error when all registries fail", func(t *testing.T) {
		m := NewManager()

		client := &mockRegistryClientWithFuncs{
			searchFunc: func(_ context.Context, _ string) ([]*SearchResult, error) {
				return nil, errors.New("registry unavailable")
			},
		}

		m.AddRegistry("failing-registry", client, RegistryConfig{})

		_, err := m.Search(ctx, "tool")
		if err == nil {
			t.Fatal("expected error when all registries fail")
		}
	})
}

// TestSyncLoop tests the background sync functionality.
// **Validates: Requirements 2.3, 5.3**
func TestSyncLoop(t *testing.T) {
	ctx := context.Background()

	t.Run("starts and stops sync loop", func(t *testing.T) {
		m := NewManager()

		client := &mockRegistryClient{
			toolsets: []*ToolsetInfo{
				{ID: "ts-1", Name: "sync-toolset"},
			},
		}

		m.AddRegistry("test-registry", client, RegistryConfig{
			SyncInterval: 100 * time.Millisecond,
		})

		err := m.StartSync(ctx)
		if err != nil {
			t.Fatalf("StartSync failed: %v", err)
		}

		// Let sync run briefly
		time.Sleep(50 * time.Millisecond)

		m.StopSync()
		// Test passes if no panic or deadlock
	})

	t.Run("prevents double start", func(t *testing.T) {
		m := NewManager()

		client := &mockRegistryClient{}
		m.AddRegistry("test-registry", client, RegistryConfig{
			SyncInterval: time.Hour,
		})

		err := m.StartSync(ctx)
		if err != nil {
			t.Fatalf("first StartSync failed: %v", err)
		}
		defer m.StopSync()

		err = m.StartSync(ctx)
		if err == nil {
			t.Fatal("expected error on double start")
		}
	})

	t.Run("syncs registry at interval", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))

		var mu sync.Mutex
		listCallCount := 0
		client := &mockRegistryClientWithFuncs{
			listToolsetsFunc: func(_ context.Context) ([]*ToolsetInfo, error) {
				mu.Lock()
				listCallCount++
				mu.Unlock()
				return []*ToolsetInfo{
					{ID: "ts-1", Name: "synced-toolset"},
				}, nil
			},
			getToolsetFunc: func(_ context.Context, _ string) (*ToolsetSchema, error) {
				return &ToolsetSchema{ID: "ts-1", Name: "synced-toolset"}, nil
			},
		}

		m.AddRegistry("test-registry", client, RegistryConfig{
			SyncInterval: 50 * time.Millisecond,
			CacheTTL:     time.Hour,
		})

		err := m.StartSync(ctx)
		if err != nil {
			t.Fatalf("StartSync failed: %v", err)
		}

		// Wait for initial sync plus one interval
		time.Sleep(120 * time.Millisecond)

		m.StopSync()

		mu.Lock()
		count := listCallCount
		mu.Unlock()

		// Should have at least 2 calls (initial + 1 interval)
		if count < 2 {
			t.Errorf("expected at least 2 sync calls, got %d", count)
		}
	})

	t.Run("skips registries without sync interval", func(t *testing.T) {
		m := NewManager()

		var mu sync.Mutex
		callCount := 0
		client := &mockRegistryClientWithFuncs{
			listToolsetsFunc: func(_ context.Context) ([]*ToolsetInfo, error) {
				mu.Lock()
				callCount++
				mu.Unlock()
				return nil, nil
			},
		}

		// Add registry with zero sync interval
		m.AddRegistry("no-sync-registry", client, RegistryConfig{
			SyncInterval: 0,
		})

		err := m.StartSync(ctx)
		if err != nil {
			t.Fatalf("StartSync failed: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
		m.StopSync()

		mu.Lock()
		count := callCount
		mu.Unlock()

		if count != 0 {
			t.Errorf("expected 0 sync calls for registry without interval, got %d", count)
		}
	})
}

// TestFederationFiltering tests federation include/exclude filtering.
// **Validates: Requirements 5.1**
func TestFederationFiltering(t *testing.T) {
	ctx := context.Background()

	t.Run("includes matching patterns", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))

		client := &mockRegistryClientWithFuncs{
			listToolsetsFunc: func(_ context.Context) ([]*ToolsetInfo, error) {
				return []*ToolsetInfo{
					{ID: "1", Name: "web-search"},
					{ID: "2", Name: "code-execution"},
					{ID: "3", Name: "experimental-feature"},
				}, nil
			},
			getToolsetFunc: func(_ context.Context, name string) (*ToolsetSchema, error) {
				return &ToolsetSchema{ID: name, Name: name}, nil
			},
		}

		m.AddRegistry("federated", client, RegistryConfig{
			SyncInterval: 50 * time.Millisecond,
			CacheTTL:     time.Hour,
			Federation: &FederationConfig{
				Include: []string{"web-*", "code-*"},
			},
		})

		err := m.StartSync(ctx)
		if err != nil {
			t.Fatalf("StartSync failed: %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		m.StopSync()

		// Check that only matching toolsets were cached
		schema1, _ := cache.Get(ctx, cacheKey("federated", "web-search"))
		schema2, _ := cache.Get(ctx, cacheKey("federated", "code-execution"))
		schema3, _ := cache.Get(ctx, cacheKey("federated", "experimental-feature"))

		if schema1 == nil {
			t.Error("web-search should be included")
		}
		if schema2 == nil {
			t.Error("code-execution should be included")
		}
		if schema3 != nil {
			t.Error("experimental-feature should be excluded")
		}
	})

	t.Run("excludes matching patterns", func(t *testing.T) {
		cache := NewMemoryCache()
		m := NewManager(WithCache(cache))

		client := &mockRegistryClientWithFuncs{
			listToolsetsFunc: func(_ context.Context) ([]*ToolsetInfo, error) {
				return []*ToolsetInfo{
					{ID: "1", Name: "stable-tool"},
					{ID: "2", Name: "experimental/beta"},
				}, nil
			},
			getToolsetFunc: func(_ context.Context, name string) (*ToolsetSchema, error) {
				return &ToolsetSchema{ID: name, Name: name}, nil
			},
		}

		m.AddRegistry("federated", client, RegistryConfig{
			SyncInterval: 50 * time.Millisecond,
			CacheTTL:     time.Hour,
			Federation: &FederationConfig{
				Exclude: []string{"experimental/*"},
			},
		})

		err := m.StartSync(ctx)
		if err != nil {
			t.Fatalf("StartSync failed: %v", err)
		}

		time.Sleep(100 * time.Millisecond)
		m.StopSync()

		schema1, _ := cache.Get(ctx, cacheKey("federated", "stable-tool"))
		schema2, _ := cache.Get(ctx, cacheKey("federated", "experimental/beta"))

		if schema1 == nil {
			t.Error("stable-tool should be included")
		}
		if schema2 != nil {
			t.Error("experimental/beta should be excluded")
		}
	})
}

// TestMatchGlob tests the glob matching function.
func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		// Exact match
		{"foo", "foo", true},
		{"foo", "bar", false},

		// ** matches everything
		{"**", "anything", true},
		{"**", "nested/path", true},

		// Trailing /* matches direct children
		{"prefix/*", "prefix/child", true},
		{"prefix/*", "prefix/nested/child", false},
		{"prefix/*", "other/child", false},

		// Trailing /** matches all descendants
		{"prefix/**", "prefix/child", true},
		{"prefix/**", "prefix/nested/child", true},
		{"prefix/**", "other/child", false},

		// Trailing * matches prefix
		{"web-*", "web-search", true},
		{"web-*", "code-search", false},
		{"code-*", "code-execution", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.name, func(t *testing.T) {
			got := matchGlob(tt.pattern, tt.name)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
			}
		})
	}
}

// TestCacheKey tests cache key generation.
func TestCacheKey(t *testing.T) {
	key := cacheKey("my-registry", "my-toolset")
	expected := "registry/my-registry/toolset/my-toolset"
	if key != expected {
		t.Errorf("cacheKey: got %q, want %q", key, expected)
	}
}

// mockRegistryClientWithFuncs extends mockRegistryClient with configurable behavior.
// This is used in tests that need custom behavior beyond the basic mock.
type mockRegistryClientWithFuncs struct {
	toolsets         []*ToolsetInfo
	results          []*SearchResult
	listToolsetsFunc func(ctx context.Context) ([]*ToolsetInfo, error)
	getToolsetFunc   func(ctx context.Context, name string) (*ToolsetSchema, error)
	searchFunc       func(ctx context.Context, query string) ([]*SearchResult, error)
}

func (m *mockRegistryClientWithFuncs) ListToolsets(ctx context.Context) ([]*ToolsetInfo, error) {
	if m.listToolsetsFunc != nil {
		return m.listToolsetsFunc(ctx)
	}
	return m.toolsets, nil
}

func (m *mockRegistryClientWithFuncs) GetToolset(ctx context.Context, name string) (*ToolsetSchema, error) {
	if m.getToolsetFunc != nil {
		return m.getToolsetFunc(ctx, name)
	}
	for _, ts := range m.toolsets {
		if ts.Name == name {
			return &ToolsetSchema{
				ID:          ts.ID,
				Name:        ts.Name,
				Description: ts.Description,
				Version:     ts.Version,
			}, nil
		}
	}
	return nil, errors.New("toolset not found")
}

func (m *mockRegistryClientWithFuncs) Search(ctx context.Context, query string) ([]*SearchResult, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, query)
	}
	return m.results, nil
}
