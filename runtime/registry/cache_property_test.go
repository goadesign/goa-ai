package registry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestCacheFallbackOnUnavailabilityProperty verifies Property 3: Cache Fallback on Unavailability.
// **Feature: mcp-registry, Property 3: Cache Fallback on Unavailability**
// *For any* registry that becomes unavailable after successful initial fetch, the runtime
// SHALL return cached data for subsequent requests until TTL expires.
// **Validates: Requirements 1.4, 8.2**
func TestCacheFallbackOnUnavailabilityProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("cached data is returned when registry becomes unavailable", prop.ForAll(
		func(tc cacheFallbackTestCase) bool {
			ctx := context.Background()

			// Create a cache with sufficient TTL
			cache := NewMemoryCache()

			// Create a controllable mock client
			client := newControllableMockClient(&ToolsetSchema{
				ID:          tc.toolsetID,
				Name:        tc.toolsetName,
				Description: tc.description,
				Version:     tc.version,
			})

			// Create manager with the cache
			manager := NewManager(WithCache(cache))
			manager.AddRegistry(tc.registryName, client, RegistryConfig{
				CacheTTL: time.Hour, // Long TTL to ensure cache doesn't expire during test
			})

			// Step 1: Initial fetch should succeed and cache the data
			schema1, err := manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
			if err != nil {
				return false // Initial fetch should succeed
			}
			if schema1 == nil {
				return false
			}
			if schema1.ID != tc.toolsetID {
				return false
			}

			// Step 2: Make registry unavailable
			client.SetAvailable(false)

			// Step 3: Subsequent fetch should return cached data
			schema2, err := manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
			if err != nil {
				return false // Should not error - should use cache fallback
			}
			if schema2 == nil {
				return false // Should return cached data
			}

			// Property: Cached data should match original data
			if schema2.ID != tc.toolsetID {
				return false
			}
			if schema2.Name != tc.toolsetName {
				return false
			}
			if schema2.Description != tc.description {
				return false
			}
			if schema2.Version != tc.version {
				return false
			}

			return true
		},
		genCacheFallbackTestCase(),
	))

	properties.TestingRun(t)
}

// TestCacheFallbackPreservesAllFieldsProperty verifies that cache fallback preserves all schema fields.
// **Feature: mcp-registry, Property 3: Cache Fallback on Unavailability**
// **Validates: Requirements 1.4, 8.2**
func TestCacheFallbackPreservesAllFieldsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("cache fallback preserves all schema fields", prop.ForAll(
		func(tc cacheFallbackWithToolsTestCase) bool {
			ctx := context.Background()
			cache := NewMemoryCache()

			client := newControllableMockClient(tc.schema)

			manager := NewManager(WithCache(cache))
			manager.AddRegistry(tc.registryName, client, RegistryConfig{
				CacheTTL: time.Hour,
			})

			// Initial fetch
			schema1, err := manager.DiscoverToolset(ctx, tc.registryName, tc.schema.Name)
			if err != nil {
				return false
			}

			// Make unavailable
			client.SetAvailable(false)

			// Fetch from cache
			schema2, err := manager.DiscoverToolset(ctx, tc.registryName, tc.schema.Name)
			if err != nil {
				return false
			}

			// Property: All fields must be preserved
			if schema2.ID != schema1.ID {
				return false
			}
			if schema2.Name != schema1.Name {
				return false
			}
			if schema2.Description != schema1.Description {
				return false
			}
			if schema2.Version != schema1.Version {
				return false
			}
			if len(schema2.Tools) != len(schema1.Tools) {
				return false
			}

			// Check each tool is preserved
			for i, tool := range schema2.Tools {
				if tool.Name != schema1.Tools[i].Name {
					return false
				}
				if tool.Description != schema1.Tools[i].Description {
					return false
				}
			}

			return true
		},
		genCacheFallbackWithToolsTestCase(),
	))

	properties.TestingRun(t)
}

// TestCacheExpirationAfterTTLProperty verifies that cache expires after TTL.
// **Feature: mcp-registry, Property 3: Cache Fallback on Unavailability**
// **Validates: Requirements 1.4, 8.2**
func TestCacheExpirationAfterTTLProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("cache expires after TTL and returns error when registry unavailable", prop.ForAll(
		func(tc cacheFallbackTestCase) bool {
			ctx := context.Background()
			cache := NewMemoryCache()

			client := newControllableMockClient(&ToolsetSchema{
				ID:          tc.toolsetID,
				Name:        tc.toolsetName,
				Description: tc.description,
				Version:     tc.version,
			})

			// Use very short TTL for testing
			shortTTL := 50 * time.Millisecond

			manager := NewManager(WithCache(cache))
			manager.AddRegistry(tc.registryName, client, RegistryConfig{
				CacheTTL: shortTTL,
			})

			// Initial fetch
			_, err := manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
			if err != nil {
				return false
			}

			// Make unavailable
			client.SetAvailable(false)

			// Immediate fetch should use cache
			schema, err := manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
			if err != nil {
				return false // Should use cache
			}
			if schema == nil {
				return false
			}

			// Wait for TTL to expire
			time.Sleep(shortTTL + 20*time.Millisecond)

			// Now fetch should fail since cache expired and registry unavailable
			_, err = manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
			// Property: After TTL expires and registry unavailable, should return error
			return err != nil
		},
		genCacheFallbackTestCase(),
	))

	properties.TestingRun(t)
}

// TestMultipleFallbackRequestsProperty verifies multiple requests use cache when unavailable.
// **Feature: mcp-registry, Property 3: Cache Fallback on Unavailability**
// **Validates: Requirements 1.4, 8.2**
func TestMultipleFallbackRequestsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("multiple requests return cached data when registry unavailable", prop.ForAll(
		func(tc cacheFallbackTestCase, numRequests int) bool {
			ctx := context.Background()
			cache := NewMemoryCache()

			client := newControllableMockClient(&ToolsetSchema{
				ID:          tc.toolsetID,
				Name:        tc.toolsetName,
				Description: tc.description,
				Version:     tc.version,
			})

			manager := NewManager(WithCache(cache))
			manager.AddRegistry(tc.registryName, client, RegistryConfig{
				CacheTTL: time.Hour,
			})

			// Initial fetch
			_, err := manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
			if err != nil {
				return false
			}

			// Make unavailable
			client.SetAvailable(false)

			// Multiple requests should all succeed with cached data
			for range numRequests {
				schema, err := manager.DiscoverToolset(ctx, tc.registryName, tc.toolsetName)
				if err != nil {
					return false
				}
				if schema == nil {
					return false
				}
				if schema.ID != tc.toolsetID {
					return false
				}
			}

			return true
		},
		genCacheFallbackTestCase(),
		gen.IntRange(1, 10),
	))

	properties.TestingRun(t)
}

// Test types

type cacheFallbackTestCase struct {
	registryName string
	toolsetID    string
	toolsetName  string
	description  string
	version      string
}

type cacheFallbackWithToolsTestCase struct {
	registryName string
	schema       *ToolsetSchema
}

// controllableMockClient is a mock client that can be toggled between available and unavailable.
type controllableMockClient struct {
	available atomic.Bool
	schema    *ToolsetSchema
}

// newControllableMockClient creates a new controllable mock client that starts as available.
func newControllableMockClient(schema *ToolsetSchema) *controllableMockClient {
	c := &controllableMockClient{
		schema: schema,
	}
	c.available.Store(true)
	return c
}

func (c *controllableMockClient) SetAvailable(available bool) {
	c.available.Store(available)
}

func (c *controllableMockClient) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	if !c.available.Load() {
		return nil, errors.New("registry unavailable")
	}
	return []*ToolsetInfo{
		{
			ID:          c.schema.ID,
			Name:        c.schema.Name,
			Description: c.schema.Description,
			Version:     c.schema.Version,
		},
	}, nil
}

func (c *controllableMockClient) GetToolset(_ context.Context, name string) (*ToolsetSchema, error) {
	if !c.available.Load() {
		return nil, errors.New("registry unavailable")
	}
	if c.schema.Name == name {
		// Return a copy to avoid mutation
		schemaCopy := *c.schema
		if c.schema.Tools != nil {
			schemaCopy.Tools = make([]*ToolSchema, len(c.schema.Tools))
			for i, tool := range c.schema.Tools {
				toolCopy := *tool
				schemaCopy.Tools[i] = &toolCopy
			}
		}
		return &schemaCopy, nil
	}
	return nil, fmt.Errorf("toolset %q not found", name)
}

func (c *controllableMockClient) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	if !c.available.Load() {
		return nil, errors.New("registry unavailable")
	}
	return []*SearchResult{
		{
			ID:          c.schema.ID,
			Name:        c.schema.Name,
			Description: c.schema.Description,
			Type:        "toolset",
		},
	}, nil
}

// Generators

// genNonEmptyAlphaString generates a non-empty alpha string with length 1-20.
func genNonEmptyAlphaString() gopter.Gen {
	return gen.IntRange(1, 20).FlatMap(func(length any) gopter.Gen {
		return gen.SliceOfN(length.(int), gen.AlphaChar()).Map(func(chars []rune) string {
			return string(chars)
		})
	}, reflect.TypeOf(""))
}

// genAlphaStringWithMax generates an alpha string with max length.
func genAlphaStringWithMax(maxLen int) gopter.Gen {
	return gen.IntRange(0, maxLen).FlatMap(func(length any) gopter.Gen {
		return gen.SliceOfN(length.(int), gen.AlphaChar()).Map(func(chars []rune) string {
			return string(chars)
		})
	}, reflect.TypeOf(""))
}

func genCacheFallbackTestCase() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyAlphaString(),
		genNonEmptyAlphaString(),
		genNonEmptyAlphaString(),
		genAlphaStringWithMax(50),
		genVersionString(),
	).Map(func(vals []any) cacheFallbackTestCase {
		return cacheFallbackTestCase{
			registryName: vals[0].(string),
			toolsetID:    vals[1].(string),
			toolsetName:  vals[2].(string),
			description:  vals[3].(string),
			version:      vals[4].(string),
		}
	})
}

func genCacheFallbackWithToolsTestCase() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyAlphaString(),
		genToolsetSchemaForCache(),
	).Map(func(vals []any) cacheFallbackWithToolsTestCase {
		return cacheFallbackWithToolsTestCase{
			registryName: vals[0].(string),
			schema:       vals[1].(*ToolsetSchema),
		}
	})
}

func genToolsetSchemaForCache() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyAlphaString(),
		genNonEmptyAlphaString(),
		genAlphaStringWithMax(50),
		genVersionString(),
		gen.IntRange(0, 5),
	).FlatMap(func(vals any) gopter.Gen {
		v := vals.([]any)
		id := v[0].(string)
		name := v[1].(string)
		desc := v[2].(string)
		version := v[3].(string)
		numTools := v[4].(int)

		return gen.SliceOfN(numTools, genToolSchemaForCache()).Map(func(tools []*ToolSchema) *ToolsetSchema {
			return &ToolsetSchema{
				ID:          id,
				Name:        name,
				Description: desc,
				Version:     version,
				Tools:       tools,
			}
		})
	}, reflect.TypeOf(&ToolsetSchema{}))
}

func genToolSchemaForCache() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyAlphaString(),
		genAlphaStringWithMax(50),
	).Map(func(vals []any) *ToolSchema {
		return &ToolSchema{
			Name:        vals[0].(string),
			Description: vals[1].(string),
			InputSchema: []byte(`{"type":"object"}`),
		}
	})
}

func genVersionString() gopter.Gen {
	return gopter.CombineGens(
		gen.IntRange(0, 10),
		gen.IntRange(0, 20),
		gen.IntRange(0, 100),
	).Map(func(vals []any) string {
		return fmt.Sprintf("%d.%d.%d", vals[0].(int), vals[1].(int), vals[2].(int))
	})
}
