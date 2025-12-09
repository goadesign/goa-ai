package memory

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// TestRegistrationRoundTripConsistency verifies Property 1: Registration round-trip consistency.
// **Feature: internal-tool-registry, Property 1: Registration round-trip consistency**
// *For any* valid toolset registration, registering the toolset and then retrieving
// it by name should return equivalent metadata (name, description, version, tags, tools).
// **Validates: Requirements 2.1, 7.1**
func TestRegistrationRoundTripConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("save then get returns equivalent toolset", prop.ForAll(
		func(toolset *genregistry.Toolset) bool {
			st := New()
			ctx := context.Background()

			// Save the toolset
			if err := st.SaveToolset(ctx, toolset); err != nil {
				return false
			}

			// Retrieve the toolset
			retrieved, err := st.GetToolset(ctx, toolset.Name)
			if err != nil {
				return false
			}

			// Verify equivalence
			return toolsetsEqual(toolset, retrieved)
		},
		genToolset(),
	))

	properties.TestingRun(t)
}

// TestTagFilteringCorrectness verifies Property 6: Tag filtering correctness.
// **Feature: internal-tool-registry, Property 6: Tag filtering correctness**
// *For any* set of registered toolsets and tag filter, ListToolsets should return
// exactly those toolsets that have all specified tags.
// **Validates: Requirements 6.2**
func TestTagFilteringCorrectness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("tag filter returns only toolsets with all tags", prop.ForAll(
		func(toolsets []*genregistry.Toolset, filterTags []string) bool {
			st := New()
			ctx := context.Background()

			// Save all toolsets
			for _, ts := range toolsets {
				if err := st.SaveToolset(ctx, ts); err != nil {
					return false
				}
			}

			// List with tag filter
			results, err := st.ListToolsets(ctx, filterTags)
			if err != nil {
				return false
			}

			// Verify all results have all filter tags
			for _, ts := range results {
				if !hasAllTags(ts.Tags, filterTags) {
					return false
				}
			}

			// Verify no toolset with all tags was excluded
			for _, ts := range toolsets {
				if hasAllTags(ts.Tags, filterTags) {
					if !containsToolset(results, ts.Name) {
						return false
					}
				}
			}

			return true
		},
		genToolsetSlice(),
		genTagFilter(),
	))

	properties.TestingRun(t)
}

// TestSearchMatchesNameDescriptionOrTags verifies Property 7: Search matches name, description, or tags.
// **Feature: internal-tool-registry, Property 7: Search matches name, description, or tags**
// *For any* set of registered toolsets and search query, Search should return exactly
// those toolsets where the query appears in name, description, or any tag.
// **Validates: Requirements 8.1**
func TestSearchMatchesNameDescriptionOrTags(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search returns toolsets matching query in name, description, or tags", prop.ForAll(
		func(toolsets []*genregistry.Toolset, query string) bool {
			st := New()
			ctx := context.Background()

			// Save all toolsets
			for _, ts := range toolsets {
				if err := st.SaveToolset(ctx, ts); err != nil {
					return false
				}
			}

			// Search
			results, err := st.SearchToolsets(ctx, query)
			if err != nil {
				return false
			}

			// Verify all results match the query
			for _, ts := range results {
				if !matchesSearchQuery(ts, query) {
					return false
				}
			}

			// Verify no matching toolset was excluded
			for _, ts := range toolsets {
				if matchesSearchQuery(ts, query) {
					if !containsToolset(results, ts.Name) {
						return false
					}
				}
			}

			return true
		},
		genToolsetSlice(),
		genSearchQuery(),
	))

	properties.TestingRun(t)
}

// --- Helper functions ---

func toolsetsEqual(a, b *genregistry.Toolset) bool {
	if a.Name != b.Name {
		return false
	}
	if !stringPtrEqual(a.Description, b.Description) {
		return false
	}
	if !stringPtrEqual(a.Version, b.Version) {
		return false
	}
	if !stringSliceEqual(a.Tags, b.Tags) {
		return false
	}
	if a.StreamID != b.StreamID {
		return false
	}
	if a.RegisteredAt != b.RegisteredAt {
		return false
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Tools {
		if !toolsEqual(a.Tools[i], b.Tools[i]) {
			return false
		}
	}
	return true
}

func toolsEqual(a, b *genregistry.Tool) bool {
	if a.Name != b.Name {
		return false
	}
	if !stringPtrEqual(a.Description, b.Description) {
		return false
	}
	if !reflect.DeepEqual(a.InputSchema, b.InputSchema) {
		return false
	}
	if !reflect.DeepEqual(a.OutputSchema, b.OutputSchema) {
		return false
	}
	return true
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasAllTags(toolsetTags, filterTags []string) bool {
	if len(filterTags) == 0 {
		return true
	}
	tagSet := make(map[string]struct{}, len(toolsetTags))
	for _, tag := range toolsetTags {
		tagSet[tag] = struct{}{}
	}
	for _, tag := range filterTags {
		if _, ok := tagSet[tag]; !ok {
			return false
		}
	}
	return true
}

func containsToolset(toolsets []*genregistry.Toolset, name string) bool {
	for _, ts := range toolsets {
		if ts.Name == name {
			return true
		}
	}
	return false
}

func matchesSearchQuery(ts *genregistry.Toolset, query string) bool {
	lowerQuery := strings.ToLower(query)
	if strings.Contains(strings.ToLower(ts.Name), lowerQuery) {
		return true
	}
	if ts.Description != nil && strings.Contains(strings.ToLower(*ts.Description), lowerQuery) {
		return true
	}
	for _, tag := range ts.Tags {
		if strings.Contains(strings.ToLower(tag), lowerQuery) {
			return true
		}
	}
	return false
}

// --- Generators ---

func genToolset() gopter.Gen {
	return gopter.CombineGens(
		genToolsetName(),
		genOptionalString(),
		genOptionalString(),
		genTags(),
		genToolSlice(),
		genStreamID(),
		genTimestamp(),
	).Map(func(vals []any) *genregistry.Toolset {
		var desc, version *string
		if vals[1] != nil {
			desc = vals[1].(*string)
		}
		if vals[2] != nil {
			version = vals[2].(*string)
		}
		return &genregistry.Toolset{
			Name:         vals[0].(string),
			Description:  desc,
			Version:      version,
			Tags:         vals[3].([]string),
			Tools:        vals[4].([]*genregistry.Tool),
			StreamID:     vals[5].(string),
			RegisteredAt: vals[6].(string),
		}
	})
}

func genToolsetSlice() gopter.Gen {
	return gen.SliceOfN(5, genToolset()).Map(func(toolsets []*genregistry.Toolset) []*genregistry.Toolset {
		seen := make(map[string]bool)
		result := make([]*genregistry.Toolset, 0, len(toolsets))
		for i, ts := range toolsets {
			if seen[ts.Name] {
				ts.Name = ts.Name + "-" + string(rune('a'+i))
			}
			seen[ts.Name] = true
			result = append(result, ts)
		}
		return result
	})
}

func genToolsetName() gopter.Gen {
	return gen.OneConstOf(
		"data-tools",
		"analytics",
		"etl-pipeline",
		"search-service",
		"notification-tools",
	)
}

func genOptionalString() gopter.Gen {
	return gen.PtrOf(gen.OneConstOf(
		"A description",
		"Another description",
		"Tools for processing",
		"Service utilities",
	))
}

func genTags() gopter.Gen {
	return gen.SliceOfN(3, gen.OneConstOf(
		"data",
		"etl",
		"analytics",
		"search",
		"notification",
		"api",
	))
}

func genTagFilter() gopter.Gen {
	return gen.SliceOfN(2, gen.OneConstOf(
		"data",
		"etl",
		"analytics",
		"search",
	))
}

func genSearchQuery() gopter.Gen {
	return gen.OneConstOf(
		"data",
		"tool",
		"analytics",
		"search",
		"process",
		"service",
	)
}

func genToolSlice() gopter.Gen {
	return gen.SliceOfN(3, genTool()).Map(func(tools []*genregistry.Tool) []*genregistry.Tool {
		return tools
	})
}

func genTool() gopter.Gen {
	return gopter.CombineGens(
		genToolName(),
		genOptionalString(),
		genSchema(),
		genSchema(),
	).Map(func(vals []any) *genregistry.Tool {
		var desc *string
		if vals[1] != nil {
			desc = vals[1].(*string)
		}
		return &genregistry.Tool{
			Name:         vals[0].(string),
			Description:  desc,
			InputSchema:  vals[2].([]byte),
			OutputSchema: vals[3].([]byte),
		}
	})
}

func genToolName() gopter.Gen {
	return gen.OneConstOf(
		"analyze",
		"transform",
		"query",
		"notify",
		"search",
	)
}

func genSchema() gopter.Gen {
	return gen.OneConstOf(
		[]byte(`{"type":"object"}`),
		[]byte(`{"type":"string"}`),
		[]byte(`{"type":"array","items":{"type":"string"}}`),
	)
}

func genStreamID() gopter.Gen {
	return gen.OneConstOf(
		"toolset:data-tools:requests",
		"toolset:analytics:requests",
		"toolset:etl:requests",
	)
}

func genTimestamp() gopter.Gen {
	return gen.OneConstOf(
		"2024-01-15T10:30:00Z",
		"2024-02-20T14:45:00Z",
		"2024-03-10T08:00:00Z",
	)
}
