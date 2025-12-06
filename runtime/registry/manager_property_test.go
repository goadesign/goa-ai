package registry

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestRegistryCatalogMergePreservesToolsProperty verifies Property 2: Registry Catalog Merge Preserves Tools.
// **Feature: mcp-registry, Property 2: Registry Catalog Merge Preserves Tools**
// *For any* set of registry sources with non-conflicting tool names, merging their
// catalogs SHALL include all tools from all sources.
// **Validates: Requirements 1.3**
func TestRegistryCatalogMergePreservesToolsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("merged catalog contains all tools from all registries", prop.ForAll(
		func(registryConfigs []testRegistryConfig) bool {
			if len(registryConfigs) == 0 {
				return true // Empty case is trivially true
			}

			// Create manager
			manager := NewManager()

			// Track all expected tools across all registries
			expectedTools := make(map[string]bool)

			// Add each registry with its mock client
			for _, cfg := range registryConfigs {
				client := &mockRegistryClient{
					toolsets: cfg.toolsets,
					results:  cfg.searchResults,
				}
				manager.AddRegistry(cfg.name, client, RegistryConfig{})

				// Track expected tools from this registry
				for _, result := range cfg.searchResults {
					key := cfg.name + ":" + result.ID
					expectedTools[key] = true
				}
			}

			// Perform search to trigger catalog merge
			ctx := context.Background()
			results, err := manager.Search(ctx, "test")
			if err != nil {
				// All registries failed - this is acceptable if all mock clients return errors
				// But our mock clients don't return errors, so this shouldn't happen
				return false
			}

			// Build map of actual results
			actualTools := make(map[string]bool)
			for _, result := range results {
				key := result.Origin + ":" + result.ID
				actualTools[key] = true
			}

			// Property: All expected tools must be present in merged results
			for expectedKey := range expectedTools {
				if !actualTools[expectedKey] {
					return false
				}
			}

			// Property: Number of results should equal total expected tools
			if len(results) != len(expectedTools) {
				return false
			}

			return true
		},
		genRegistryConfigs(),
	))

	properties.TestingRun(t)
}

// TestMergedCatalogPreservesOrigin verifies that merged results preserve their origin registry.
// **Feature: mcp-registry, Property 2: Registry Catalog Merge Preserves Tools**
// **Validates: Requirements 1.3**
func TestMergedCatalogPreservesOrigin(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("merged results preserve origin registry", prop.ForAll(
		func(registryConfigs []testRegistryConfig) bool {
			if len(registryConfigs) == 0 {
				return true
			}

			manager := NewManager()

			// Track expected origins for each tool
			expectedOrigins := make(map[string]string)

			for _, cfg := range registryConfigs {
				client := &mockRegistryClient{
					toolsets: cfg.toolsets,
					results:  cfg.searchResults,
				}
				manager.AddRegistry(cfg.name, client, RegistryConfig{})

				for _, result := range cfg.searchResults {
					expectedOrigins[result.ID] = cfg.name
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
					return false // Unexpected result
				}
				if result.Origin != expectedOrigin {
					return false
				}
			}

			return true
		},
		genRegistryConfigs(),
	))

	properties.TestingRun(t)
}

// TestMergedCatalogPreservesAllFields verifies that merged results preserve all fields.
// **Feature: mcp-registry, Property 2: Registry Catalog Merge Preserves Tools**
// **Validates: Requirements 1.3**
func TestMergedCatalogPreservesAllFields(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("merged results preserve all fields", prop.ForAll(
		func(registryConfigs []testRegistryConfig) bool {
			if len(registryConfigs) == 0 {
				return true
			}

			manager := NewManager()

			// Track expected results by ID
			expectedResults := make(map[string]*SearchResult)

			for _, cfg := range registryConfigs {
				client := &mockRegistryClient{
					toolsets: cfg.toolsets,
					results:  cfg.searchResults,
				}
				manager.AddRegistry(cfg.name, client, RegistryConfig{})

				for _, result := range cfg.searchResults {
					// Store a copy with expected origin
					expected := *result
					expected.Origin = cfg.name
					expectedResults[result.ID] = &expected
				}
			}

			ctx := context.Background()
			results, err := manager.Search(ctx, "test")
			if err != nil {
				return false
			}

			// Property: Each result must have all fields preserved
			for _, result := range results {
				expected, exists := expectedResults[result.ID]
				if !exists {
					return false
				}

				// Check all fields are preserved
				if result.Name != expected.Name {
					return false
				}
				if result.Description != expected.Description {
					return false
				}
				if result.Type != expected.Type {
					return false
				}
				if result.SchemaRef != expected.SchemaRef {
					return false
				}
				if result.RelevanceScore != expected.RelevanceScore {
					return false
				}
			}

			return true
		},
		genRegistryConfigs(),
	))

	properties.TestingRun(t)
}

// TestEmptyRegistriesReturnEmptyResults verifies that empty registries return empty results.
// **Feature: mcp-registry, Property 2: Registry Catalog Merge Preserves Tools**
// **Validates: Requirements 1.3**
func TestEmptyRegistriesReturnEmptyResults(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("empty registries return empty results", prop.ForAll(
		func(numRegistries int) bool {
			manager := NewManager()

			// Add registries with empty results
			for i := range numRegistries {
				client := &mockRegistryClient{
					toolsets: nil,
					results:  nil,
				}
				manager.AddRegistry(fmt.Sprintf("registry-%d", i), client, RegistryConfig{})
			}

			ctx := context.Background()
			results, err := manager.Search(ctx, "test")
			if err != nil {
				return false
			}

			// Property: Empty registries should return empty results
			return len(results) == 0
		},
		gen.IntRange(0, 5),
	))

	properties.TestingRun(t)
}

// Test types and helpers

type testRegistryConfig struct {
	name          string
	toolsets      []*ToolsetInfo
	searchResults []*SearchResult
}

// mockRegistryClient implements RegistryClient for testing.
type mockRegistryClient struct {
	toolsets []*ToolsetInfo
	results  []*SearchResult
}

func (m *mockRegistryClient) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	return m.toolsets, nil
}

func (m *mockRegistryClient) GetToolset(_ context.Context, name string) (*ToolsetSchema, error) {
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
	return nil, fmt.Errorf("toolset %q not found", name)
}

func (m *mockRegistryClient) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	return m.results, nil
}

// Generators

// genRegistryConfigs generates a slice of registry configurations for property testing.
// Uses a combination of random number of registries and random number of results per registry.
func genRegistryConfigs() gopter.Gen {
	return gen.IntRange(1, 4).FlatMap(func(numRegistries any) gopter.Gen {
		n := numRegistries.(int)
		// Generate random number of results for each registry
		return gen.SliceOfN(n, gen.IntRange(0, 5)).Map(func(resultCounts []int) []testRegistryConfig {
			configs := make([]testRegistryConfig, n)
			for i := range n {
				configs[i] = genRegistryConfigValue(i, resultCounts[i])
			}
			return configs
		})
	}, reflect.TypeOf([]testRegistryConfig{}))
}

// genRegistryConfigValue generates a single registry configuration value.
// Uses registryIndex to ensure unique tool IDs across registries.
func genRegistryConfigValue(registryIndex, numResults int) testRegistryConfig {
	registryName := fmt.Sprintf("registry-%d", registryIndex)
	results := make([]*SearchResult, numResults)
	for i := range numResults {
		results[i] = &SearchResult{
			ID:             fmt.Sprintf("tool-%d-%d", registryIndex, i),
			Name:           genToolNameValue(i),
			Description:    genToolDescriptionValue(i),
			Type:           genTypeValue(i),
			SchemaRef:      fmt.Sprintf("/schemas/tool-%d-%d", registryIndex, i),
			RelevanceScore: float64(i+1) * 0.2,
			Tags:           genTagsValue(i),
			Origin:         "", // Will be set by manager
		}
	}
	return testRegistryConfig{
		name:          registryName,
		toolsets:      nil,
		searchResults: results,
	}
}

// genToolNameValue returns a tool name based on index.
func genToolNameValue(index int) string {
	names := []string{"analyze", "search", "query", "process", "transform"}
	return names[index%len(names)]
}

// genToolDescriptionValue returns a tool description based on index.
func genToolDescriptionValue(index int) string {
	descs := []string{"Analyzes data", "Searches for items", "Queries the database", "Processes input", "Transforms data format"}
	return descs[index%len(descs)]
}

// genTypeValue returns a type based on index.
func genTypeValue(index int) string {
	types := []string{"tool", "toolset", "agent"}
	return types[index%len(types)]
}

// genTagsValue returns tags based on index.
func genTagsValue(index int) []string {
	tagSets := [][]string{
		{},
		{"data"},
		{"data", "analytics"},
		{"admin", "config", "system"},
	}
	return tagSets[index%len(tagSets)]
}
