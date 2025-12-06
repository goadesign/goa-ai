package registry

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestFederationOriginTaggingProperty verifies Property 8: Federation Origin Tagging.
// **Feature: mcp-registry, Property 8: Federation Origin Tagging**
// *For any* federated server or agent imported from an external registry, the runtime
// SHALL tag it with its origin source.
// **Validates: Requirements 5.2**
func TestFederationOriginTaggingProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("federated toolsets are tagged with origin registry", prop.ForAll(
		func(cfg federationTestConfig) bool {
			if len(cfg.toolsets) == 0 {
				return true // Empty case is trivially true
			}

			// Create manager with a memory cache to capture synced data
			cache := NewMemoryCache()
			manager := NewManager(WithCache(cache))

			// Create mock client that returns the toolsets
			client := &federationMockClient{
				toolsets: cfg.toolsets,
			}

			// Add registry with federation config
			manager.AddRegistry(cfg.registryName, client, RegistryConfig{
				SyncInterval: time.Hour, // Long interval, we'll sync manually
				CacheTTL:     time.Hour,
				Federation:   cfg.federation,
			})

			// Manually trigger sync by calling doSync
			manager.mu.RLock()
			entry := manager.registries[cfg.registryName]
			manager.mu.RUnlock()

			// Set up sync context
			manager.syncCtx = context.Background()
			manager.doSync(cfg.registryName, entry)

			// Verify all cached toolsets have origin set
			ctx := context.Background()
			for _, ts := range cfg.toolsets {
				// Skip toolsets that should be filtered out
				if cfg.federation != nil && !manager.shouldInclude(ts.Name, cfg.federation) {
					continue
				}

				key := cacheKey(cfg.registryName, ts.Name)
				cached, err := cache.Get(ctx, key)
				if err != nil {
					t.Logf("cache get error for %s: %v", key, err)
					return false
				}
				if cached == nil {
					t.Logf("cached schema is nil for %s", key)
					return false
				}

				// Property: Origin must be set to the registry name
				if cached.Origin != cfg.registryName {
					t.Logf("origin mismatch: got %q, want %q", cached.Origin, cfg.registryName)
					return false
				}
			}

			return true
		},
		genFederationTestConfig(),
	))

	properties.TestingRun(t)
}

// TestFederationOriginTaggingOnDiscoverProperty verifies that DiscoverToolset also tags origin.
// **Feature: mcp-registry, Property 8: Federation Origin Tagging**
// **Validates: Requirements 5.2**
func TestFederationOriginTaggingOnDiscoverProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("discovered toolsets are tagged with origin registry", prop.ForAll(
		func(cfg federationTestConfig) bool {
			if len(cfg.toolsets) == 0 {
				return true
			}

			manager := NewManager()

			client := &federationMockClient{
				toolsets: cfg.toolsets,
			}

			manager.AddRegistry(cfg.registryName, client, RegistryConfig{
				CacheTTL:   time.Hour,
				Federation: cfg.federation,
			})

			ctx := context.Background()

			// Discover each toolset and verify origin is set
			for _, ts := range cfg.toolsets {
				schema, err := manager.DiscoverToolset(ctx, cfg.registryName, ts.Name)
				if err != nil {
					t.Logf("discover error for %s: %v", ts.Name, err)
					return false
				}

				// Property: Origin must be set to the registry name
				if schema.Origin != cfg.registryName {
					t.Logf("origin mismatch: got %q, want %q", schema.Origin, cfg.registryName)
					return false
				}
			}

			return true
		},
		genFederationTestConfig(),
	))

	properties.TestingRun(t)
}

// TestFederationOriginTaggingOnSearchProperty verifies that Search results are tagged with origin.
// **Feature: mcp-registry, Property 8: Federation Origin Tagging**
// **Validates: Requirements 5.2**
func TestFederationOriginTaggingOnSearchProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search results from federated registries are tagged with origin", prop.ForAll(
		func(cfgs []federationTestConfig) bool {
			if len(cfgs) == 0 {
				return true
			}

			manager := NewManager()

			// Track expected origins for each result
			expectedOrigins := make(map[string]string)

			for _, cfg := range cfgs {
				client := &federationMockClient{
					toolsets:      cfg.toolsets,
					searchResults: cfg.searchResults,
				}

				manager.AddRegistry(cfg.registryName, client, RegistryConfig{
					Federation: cfg.federation,
				})

				for _, result := range cfg.searchResults {
					expectedOrigins[result.ID] = cfg.registryName
				}
			}

			ctx := context.Background()
			results, err := manager.Search(ctx, "test")
			if err != nil {
				return false
			}

			// Property: Each result's Origin must match the registry it came from
			for _, result := range results {
				expectedOrigin, exists := expectedOrigins[result.ID]
				if !exists {
					t.Logf("unexpected result ID: %s", result.ID)
					return false
				}
				if result.Origin != expectedOrigin {
					t.Logf("origin mismatch for %s: got %q, want %q", result.ID, result.Origin, expectedOrigin)
					return false
				}
			}

			return true
		},
		genFederationTestConfigs(),
	))

	properties.TestingRun(t)
}

// TestFederationFilteringPreservesOriginProperty verifies that filtered toolsets still have origin.
// **Feature: mcp-registry, Property 8: Federation Origin Tagging**
// **Validates: Requirements 5.2**
func TestFederationFilteringPreservesOriginProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("filtered federated toolsets preserve origin tagging", prop.ForAll(
		func(cfg federationTestConfig) bool {
			if len(cfg.toolsets) == 0 || cfg.federation == nil {
				return true
			}

			cache := NewMemoryCache()
			manager := NewManager(WithCache(cache))

			client := &federationMockClient{
				toolsets: cfg.toolsets,
			}

			manager.AddRegistry(cfg.registryName, client, RegistryConfig{
				SyncInterval: time.Hour,
				CacheTTL:     time.Hour,
				Federation:   cfg.federation,
			})

			// Manually trigger sync
			manager.mu.RLock()
			entry := manager.registries[cfg.registryName]
			manager.mu.RUnlock()

			manager.syncCtx = context.Background()
			manager.doSync(cfg.registryName, entry)

			ctx := context.Background()

			// Count how many toolsets should be included after filtering
			includedCount := 0
			for _, ts := range cfg.toolsets {
				if manager.shouldInclude(ts.Name, cfg.federation) {
					includedCount++

					key := cacheKey(cfg.registryName, ts.Name)
					cached, err := cache.Get(ctx, key)
					if err != nil {
						return false
					}
					if cached == nil {
						return false
					}

					// Property: Included toolsets must have origin set
					if cached.Origin != cfg.registryName {
						return false
					}
				}
			}

			// Verify excluded toolsets are not cached
			for _, ts := range cfg.toolsets {
				if !manager.shouldInclude(ts.Name, cfg.federation) {
					key := cacheKey(cfg.registryName, ts.Name)
					cached, _ := cache.Get(ctx, key)
					// Excluded toolsets should not be in cache
					if cached != nil {
						return false
					}
				}
			}

			return true
		},
		genFederationTestConfigWithFilters(),
	))

	properties.TestingRun(t)
}

// Test types

type federationTestConfig struct {
	registryName  string
	toolsets      []*ToolsetInfo
	searchResults []*SearchResult
	federation    *FederationConfig
}

// federationMockClient implements RegistryClient for federation testing.
type federationMockClient struct {
	toolsets      []*ToolsetInfo
	searchResults []*SearchResult
}

func (m *federationMockClient) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	return m.toolsets, nil
}

func (m *federationMockClient) GetToolset(_ context.Context, name string) (*ToolsetSchema, error) {
	for _, ts := range m.toolsets {
		if ts.Name == name {
			return &ToolsetSchema{
				ID:          ts.ID,
				Name:        ts.Name,
				Description: ts.Description,
				Version:     ts.Version,
				Tools:       nil,
				Origin:      "", // Origin should be set by manager
			}, nil
		}
	}
	return nil, fmt.Errorf("toolset %q not found", name)
}

func (m *federationMockClient) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	return m.searchResults, nil
}

// Generators

// genFederationTestConfig generates a single federation test configuration.
func genFederationTestConfig() gopter.Gen {
	return gen.IntRange(1, 5).FlatMap(func(numToolsets any) gopter.Gen {
		n := numToolsets.(int)
		return gen.IntRange(0, 3).Map(func(registryIndex int) federationTestConfig {
			return genFederationTestConfigValue(registryIndex, n, nil)
		})
	}, reflect.TypeOf(federationTestConfig{}))
}

// genFederationTestConfigs generates multiple federation test configurations.
func genFederationTestConfigs() gopter.Gen {
	return gen.IntRange(1, 3).FlatMap(func(numRegistries any) gopter.Gen {
		n := numRegistries.(int)
		return gen.SliceOfN(n, gen.IntRange(1, 4)).Map(func(toolsetCounts []int) []federationTestConfig {
			configs := make([]federationTestConfig, n)
			for i := range n {
				configs[i] = genFederationTestConfigValue(i, toolsetCounts[i], nil)
				// Add search results
				configs[i].searchResults = genSearchResultsValue(i, toolsetCounts[i])
			}
			return configs
		})
	}, reflect.TypeOf([]federationTestConfig{}))
}

// genFederationTestConfigWithFilters generates configs with federation filters.
func genFederationTestConfigWithFilters() gopter.Gen {
	return gen.IntRange(2, 6).FlatMap(func(numToolsets any) gopter.Gen {
		n := numToolsets.(int)
		return gen.IntRange(0, 3).Map(func(registryIndex int) federationTestConfig {
			// Create federation config with include/exclude patterns
			federation := &FederationConfig{
				Include: []string{"data-*", "analytics-*"},
				Exclude: []string{"*-internal"},
			}
			return genFederationTestConfigValue(registryIndex, n, federation)
		})
	}, reflect.TypeOf(federationTestConfig{}))
}

// genFederationTestConfigValue generates a single federation test config value.
func genFederationTestConfigValue(registryIndex, numToolsets int, federation *FederationConfig) federationTestConfig {
	registryName := fmt.Sprintf("federated-registry-%d", registryIndex)
	toolsets := make([]*ToolsetInfo, numToolsets)

	// Generate toolsets with names that may or may not match federation filters
	namePatterns := []string{
		"data-tools",
		"analytics-dashboard",
		"admin-internal",
		"web-search",
		"code-execution",
		"data-internal",
	}

	for i := range numToolsets {
		name := namePatterns[i%len(namePatterns)]
		toolsets[i] = &ToolsetInfo{
			ID:          fmt.Sprintf("toolset-%d-%d", registryIndex, i),
			Name:        name,
			Description: fmt.Sprintf("Toolset %d from registry %d", i, registryIndex),
			Version:     "1.0.0",
			Tags:        []string{"federated"},
			Origin:      "", // Origin should be set by manager
		}
	}

	return federationTestConfig{
		registryName: registryName,
		toolsets:     toolsets,
		federation:   federation,
	}
}

// genSearchResultsValue generates search results for a registry.
func genSearchResultsValue(registryIndex, numResults int) []*SearchResult {
	results := make([]*SearchResult, numResults)
	for i := range numResults {
		results[i] = &SearchResult{
			ID:             fmt.Sprintf("result-%d-%d", registryIndex, i),
			Name:           fmt.Sprintf("tool-%d", i),
			Description:    fmt.Sprintf("Tool %d from registry %d", i, registryIndex),
			Type:           "tool",
			SchemaRef:      fmt.Sprintf("/schemas/tool-%d-%d", registryIndex, i),
			RelevanceScore: float64(i+1) * 0.2,
			Tags:           []string{"federated"},
			Origin:         "", // Origin should be set by manager
		}
	}
	return results
}
