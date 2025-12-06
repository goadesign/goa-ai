package registry

import (
	"context"
	"reflect"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestSearchReturnsRequiredFieldsProperty verifies Property 9: Search Returns Required Fields.
// **Feature: mcp-registry, Property 9: Search Returns Required Fields**
// *For any* semantic search result, the result SHALL include tool ID, description,
// schema reference, and relevance score.
// **Validates: Requirements 4.2**
func TestSearchReturnsRequiredFieldsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search results contain all required fields", prop.ForAll(
		func(searchResults []*SearchResult) bool {
			if len(searchResults) == 0 {
				return true // Empty case is trivially true
			}

			// Create manager with mock client that returns the generated results
			manager := NewManager()
			client := &mockSearchClient{
				results: searchResults,
			}
			manager.AddRegistry("test-registry", client, RegistryConfig{})

			// Perform search
			ctx := context.Background()
			results, err := manager.Search(ctx, "test query")
			if err != nil {
				return false
			}

			// Property: Every result must have all required fields per Requirements 4.2
			for _, result := range results {
				// Required field: ID (tool ID)
				if result.ID == "" {
					return false
				}

				// Required field: Description
				if result.Description == "" {
					return false
				}

				// Required field: SchemaRef (schema reference)
				if result.SchemaRef == "" {
					return false
				}

				// Required field: RelevanceScore
				// Note: 0.0 is a valid score, so we check that it's been set
				// by ensuring the result exists (which it does if we got here)
				// The score must be in valid range [0.0, 1.0]
				if result.RelevanceScore < 0.0 || result.RelevanceScore > 1.0 {
					return false
				}
			}

			return true
		},
		genSearchResultsWithRequiredFields(),
	))

	properties.TestingRun(t)
}

// TestSearchResultsPreserveAllFields verifies that search results preserve all fields.
// **Feature: mcp-registry, Property 9: Search Returns Required Fields**
// **Validates: Requirements 4.2**
func TestSearchResultsPreserveAllFields(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search results preserve all fields from source", prop.ForAll(
		func(searchResults []*SearchResult) bool {
			if len(searchResults) == 0 {
				return true
			}

			// Create manager with mock client
			manager := NewManager()
			client := &mockSearchClient{
				results: searchResults,
			}
			manager.AddRegistry("test-registry", client, RegistryConfig{})

			// Build expected results map
			expectedByID := make(map[string]*SearchResult)
			for _, r := range searchResults {
				expectedByID[r.ID] = r
			}

			// Perform search
			ctx := context.Background()
			results, err := manager.Search(ctx, "test query")
			if err != nil {
				return false
			}

			// Property: All fields must be preserved
			for _, result := range results {
				expected, exists := expectedByID[result.ID]
				if !exists {
					return false
				}

				// Check required fields are preserved
				if result.ID != expected.ID {
					return false
				}
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
		genSearchResultsWithRequiredFields(),
	))

	properties.TestingRun(t)
}

// TestParseSearchResultsValidatesRequiredFields verifies that ParseSearchResults
// properly validates required fields.
// **Feature: mcp-registry, Property 9: Search Returns Required Fields**
// **Validates: Requirements 4.2**
func TestParseSearchResultsValidatesRequiredFields(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ParseSearchResults filters out results without ID", prop.ForAll(
		func(results []*SearchResult) bool {
			// Mix in some results without ID
			mixedResults := make([]*SearchResult, 0, len(results)*2)
			for _, r := range results {
				mixedResults = append(mixedResults, r)
				// Add a result without ID
				mixedResults = append(mixedResults, &SearchResult{
					ID:             "", // Missing ID
					Name:           r.Name,
					Description:    r.Description,
					SchemaRef:      r.SchemaRef,
					RelevanceScore: r.RelevanceScore,
				})
			}

			parsed := ParseSearchResults(mixedResults)

			// Property: All parsed results must have an ID
			for _, result := range parsed {
				if result.ID == "" {
					return false
				}
			}

			// Property: Results without ID should be filtered out
			// So parsed length should equal original results length
			return len(parsed) == len(results)
		},
		genSearchResultsWithRequiredFields(),
	))

	properties.TestingRun(t)
}

// TestSearchResultsHaveValidRelevanceScores verifies relevance scores are in valid range.
// **Feature: mcp-registry, Property 9: Search Returns Required Fields**
// **Validates: Requirements 4.2**
func TestSearchResultsHaveValidRelevanceScores(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("relevance scores are in valid range [0.0, 1.0]", prop.ForAll(
		func(searchResults []*SearchResult) bool {
			if len(searchResults) == 0 {
				return true
			}

			manager := NewManager()
			client := &mockSearchClient{
				results: searchResults,
			}
			manager.AddRegistry("test-registry", client, RegistryConfig{})

			ctx := context.Background()
			results, err := manager.Search(ctx, "test query")
			if err != nil {
				return false
			}

			// Property: All relevance scores must be in [0.0, 1.0]
			for _, result := range results {
				if result.RelevanceScore < 0.0 || result.RelevanceScore > 1.0 {
					return false
				}
			}

			return true
		},
		genSearchResultsWithRequiredFields(),
	))

	properties.TestingRun(t)
}

// mockSearchClient implements RegistryClient for search testing.
type mockSearchClient struct {
	results []*SearchResult
}

func (m *mockSearchClient) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	return nil, nil
}

func (m *mockSearchClient) GetToolset(_ context.Context, _ string) (*ToolsetSchema, error) {
	return nil, nil
}

func (m *mockSearchClient) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	return m.results, nil
}

// Generators

// genSearchResultsWithRequiredFields generates search results with all required fields populated.
func genSearchResultsWithRequiredFields() gopter.Gen {
	return gen.IntRange(0, 10).FlatMap(func(n any) gopter.Gen {
		count := n.(int)
		return gen.SliceOfN(count, genSearchResult()).Map(func(results []*SearchResult) []*SearchResult {
			// Ensure unique IDs
			for i, r := range results {
				r.ID = genUniqueID(i)
			}
			return results
		})
	}, reflect.TypeOf([]*SearchResult{}))
}

// genSearchResult generates a single search result with all required fields.
func genSearchResult() gopter.Gen {
	return gopter.CombineGens(
		gen.AlphaString(),          // Name
		gen.AlphaString(),          // Description (will ensure non-empty)
		genResultType(),            // Type
		gen.AlphaString(),          // SchemaRef (will ensure non-empty)
		gen.Float64Range(0.0, 1.0), // RelevanceScore
		genTags(),                  // Tags
	).Map(func(vals []any) *SearchResult {
		name := vals[0].(string)
		description := vals[1].(string)
		resultType := vals[2].(string)
		schemaRef := vals[3].(string)
		relevanceScore := vals[4].(float64)
		tags := vals[5].([]string)

		// Ensure required fields are non-empty
		if description == "" {
			description = "Default description"
		}
		if schemaRef == "" {
			schemaRef = "/schemas/default"
		}

		return &SearchResult{
			ID:             "", // Will be set by caller to ensure uniqueness
			Name:           name,
			Description:    description,
			Type:           resultType,
			SchemaRef:      schemaRef,
			RelevanceScore: relevanceScore,
			Tags:           tags,
			Origin:         "", // Will be set by manager
		}
	})
}

// genResultType generates a valid result type.
func genResultType() gopter.Gen {
	return gen.OneConstOf("tool", "toolset", "agent")
}

// genTags generates a slice of tags.
func genTags() gopter.Gen {
	return gen.SliceOfN(3, gen.AlphaString()).Map(func(tags []string) []string {
		// Filter out empty tags
		result := make([]string, 0, len(tags))
		for _, tag := range tags {
			if tag != "" {
				result = append(result, tag)
			}
		}
		return result
	})
}

// genUniqueID generates a unique ID based on index.
func genUniqueID(index int) string {
	return "tool-" + string(rune('a'+index%26)) + "-" + string(rune('0'+index/26))
}

// TestEmptySearchReturnsEmptySetProperty verifies Property 10: Empty Search Returns Empty Set.
// **Feature: mcp-registry, Property 10: Empty Search Returns Empty Set**
// *For any* search query with no matches, the runtime SHALL return an empty result set without error.
// **Validates: Requirements 4.3**
func TestEmptySearchReturnsEmptySetProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("empty search returns empty set without error", prop.ForAll(
		func(query string) bool {
			// Create manager with mock client that returns no results
			manager := NewManager()
			client := &mockEmptySearchClient{}
			manager.AddRegistry("test-registry", client, RegistryConfig{})

			// Perform search with any query
			ctx := context.Background()
			results, err := manager.Search(ctx, query)

			// Property: Empty search must return empty set without error
			// 1. No error should be returned
			if err != nil {
				return false
			}

			// 2. Results must be empty (nil or zero-length slice)
			if len(results) != 0 {
				return false
			}

			return true
		},
		gen.AlphaString(), // Generate arbitrary query strings
	))

	properties.TestingRun(t)
}

// TestEmptySearchWithSearchClientProperty verifies Property 10 using SearchClient.
// **Feature: mcp-registry, Property 10: Empty Search Returns Empty Set**
// *For any* search query with no matches, the runtime SHALL return an empty result set without error.
// **Validates: Requirements 4.3**
func TestEmptySearchWithSearchClientProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("SearchClient returns empty set without error for no matches", prop.ForAll(
		func(query string, opts SearchOptions) bool {
			// Create manager with mock client that returns no results
			manager := NewManager()
			client := &mockEmptySearchClient{}
			manager.AddRegistry("test-registry", client, RegistryConfig{})

			// Create search client
			searchClient := NewSearchClient(manager)

			// Perform search with any query and options
			ctx := context.Background()
			results, err := searchClient.Search(ctx, query, opts)

			// Property: Empty search must return empty set without error
			// 1. No error should be returned
			if err != nil {
				return false
			}

			// 2. Results must be empty (nil or zero-length slice)
			if len(results) != 0 {
				return false
			}

			return true
		},
		gen.AlphaString(),
		genSearchOptions(),
	))

	properties.TestingRun(t)
}

// TestEmptySearchWithNoRegistriesProperty verifies Property 10 when no registries are configured.
// **Feature: mcp-registry, Property 10: Empty Search Returns Empty Set**
// *For any* search query when no registries are configured, the runtime SHALL return an empty result set without error.
// **Validates: Requirements 4.3**
func TestEmptySearchWithNoRegistriesProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search with no registries returns empty set without error", prop.ForAll(
		func(query string) bool {
			// Create manager with no registries
			manager := NewManager()

			// Perform search
			ctx := context.Background()
			results, err := manager.Search(ctx, query)

			// Property: Search with no registries must return empty set without error
			if err != nil {
				return false
			}
			if len(results) != 0 {
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestEmptySearchWithFiltersProperty verifies Property 10 when filters exclude all results.
// **Feature: mcp-registry, Property 10: Empty Search Returns Empty Set**
// *For any* search query where filters exclude all results, the runtime SHALL return an empty result set without error.
// **Validates: Requirements 4.3**
func TestEmptySearchWithFiltersProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search with excluding filters returns empty set without error", prop.ForAll(
		func(query string, results []*SearchResult) bool {
			if len(results) == 0 {
				return true // Trivially true for empty input
			}

			// Create manager with mock client that returns results
			manager := NewManager()
			client := &mockSearchClient{
				results: results,
			}
			manager.AddRegistry("test-registry", client, RegistryConfig{})

			// Create search client with filters that exclude all results
			searchClient := NewSearchClient(manager)

			// Use a minimum relevance of 2.0 (impossible since max is 1.0)
			// This ensures all results are filtered out
			ctx := context.Background()
			filteredResults, err := searchClient.Search(ctx, query, SearchOptions{
				MinRelevance: 2.0, // Impossible threshold
			})

			// Property: Filtered search must return empty set without error
			if err != nil {
				return false
			}
			if len(filteredResults) != 0 {
				return false
			}

			return true
		},
		gen.AlphaString(),
		genSearchResultsWithRequiredFields(),
	))

	properties.TestingRun(t)
}

// mockEmptySearchClient implements RegistryClient that always returns empty results.
type mockEmptySearchClient struct{}

func (m *mockEmptySearchClient) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	return nil, nil
}

func (m *mockEmptySearchClient) GetToolset(_ context.Context, _ string) (*ToolsetSchema, error) {
	return nil, nil
}

func (m *mockEmptySearchClient) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	return []*SearchResult{}, nil
}

// genSearchOptions generates random SearchOptions for property testing.
func genSearchOptions() gopter.Gen {
	return gopter.CombineGens(
		gen.SliceOfN(3, gen.AlphaString()), // Registries
		gen.SliceOfN(3, genResultType()),   // Types
		gen.SliceOfN(3, gen.AlphaString()), // Tags
		gen.Float64Range(0.0, 1.0),         // MinRelevance
		gen.IntRange(0, 100),               // MaxResults
		gen.Bool(),                         // PreferSemantic
	).Map(func(vals []any) SearchOptions {
		return SearchOptions{
			Registries:     vals[0].([]string),
			Types:          vals[1].([]string),
			Tags:           vals[2].([]string),
			MinRelevance:   vals[3].(float64),
			MaxResults:     vals[4].(int),
			PreferSemantic: vals[5].(bool),
		}
	})
}
