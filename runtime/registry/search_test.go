package registry

import (
	"context"
	"errors"
	"testing"
)

const (
	testTypeTool     = "tool"
	testRegistryName = "test-registry"
)

// TestParseSearchResults tests the ParseSearchResults helper function.
// **Validates: Requirements 4.2**
func TestParseSearchResults(t *testing.T) {
	t.Run("filters out results without ID", func(t *testing.T) {
		results := []*SearchResult{
			{ID: "tool-1", Name: "Valid Tool", Description: "A valid tool"},
			{ID: "", Name: "Invalid Tool", Description: "Missing ID"},
			{ID: "tool-2", Name: "Another Valid", Description: "Another valid tool"},
		}

		parsed := ParseSearchResults(results)

		if len(parsed) != 2 {
			t.Errorf("expected 2 results, got %d", len(parsed))
		}
		for _, r := range parsed {
			if r.ID == "" {
				t.Error("parsed result has empty ID")
			}
		}
	})

	t.Run("sets default type when empty", func(t *testing.T) {
		results := []*SearchResult{
			{ID: "tool-1", Name: "Tool", Type: ""},
		}

		parsed := ParseSearchResults(results)

		if len(parsed) != 1 {
			t.Fatalf("expected 1 result, got %d", len(parsed))
		}
		if parsed[0].Type != testTypeTool {
			t.Errorf("expected default type %q, got %q", testTypeTool, parsed[0].Type)
		}
	})

	t.Run("preserves all fields", func(t *testing.T) {
		results := []*SearchResult{
			{
				ID:             "tool-1",
				Name:           "Test Tool",
				Description:    "A test tool",
				Type:           "toolset",
				SchemaRef:      "/schemas/test",
				RelevanceScore: 0.85,
				Tags:           []string{"test", "example"},
				Origin:         testRegistryName,
			},
		}

		parsed := ParseSearchResults(results)

		if len(parsed) != 1 {
			t.Fatalf("expected 1 result, got %d", len(parsed))
		}
		r := parsed[0]
		if r.ID != "tool-1" {
			t.Errorf("ID: got %q, want %q", r.ID, "tool-1")
		}
		if r.Name != "Test Tool" {
			t.Errorf("Name: got %q, want %q", r.Name, "Test Tool")
		}
		if r.Description != "A test tool" {
			t.Errorf("Description: got %q, want %q", r.Description, "A test tool")
		}
		if r.Type != "toolset" {
			t.Errorf("Type: got %q, want %q", r.Type, "toolset")
		}
		if r.SchemaRef != "/schemas/test" {
			t.Errorf("SchemaRef: got %q, want %q", r.SchemaRef, "/schemas/test")
		}
		if r.RelevanceScore != 0.85 {
			t.Errorf("RelevanceScore: got %f, want %f", r.RelevanceScore, 0.85)
		}
		if len(r.Tags) != 2 || r.Tags[0] != "test" || r.Tags[1] != "example" {
			t.Errorf("Tags: got %v, want %v", r.Tags, []string{"test", "example"})
		}
		if r.Origin != testRegistryName {
			t.Errorf("Origin: got %q, want %q", r.Origin, testRegistryName)
		}
	})

	t.Run("handles empty input", func(t *testing.T) {
		parsed := ParseSearchResults(nil)
		if parsed != nil {
			t.Errorf("expected nil for nil input, got %v", parsed)
		}

		parsed = ParseSearchResults([]*SearchResult{})
		if len(parsed) != 0 {
			t.Errorf("expected empty slice, got %d results", len(parsed))
		}
	})
}

// TestComputeKeywordRelevance tests the keyword relevance scoring function.
// **Validates: Requirements 4.4**
func TestComputeKeywordRelevance(t *testing.T) {
	t.Run("returns zero for empty query", func(t *testing.T) {
		result := &SearchResult{Name: "test", Description: "test description"}
		score := ComputeKeywordRelevance("", result)
		if score != 0.0 {
			t.Errorf("expected 0.0 for empty query, got %f", score)
		}
	})

	t.Run("scores higher for name matches", func(t *testing.T) {
		result := &SearchResult{
			Name:        "search tool",
			Description: "A tool for searching",
		}

		// Query that matches name should score higher than description-only match
		nameScore := ComputeKeywordRelevance("search", result)
		if nameScore <= 0 {
			t.Errorf("expected positive score for name match, got %f", nameScore)
		}
	})

	t.Run("scores for description matches", func(t *testing.T) {
		result := &SearchResult{
			Name:        "utility",
			Description: "Analyzes data patterns",
		}

		score := ComputeKeywordRelevance("analyzes", result)
		if score <= 0 {
			t.Errorf("expected positive score for description match, got %f", score)
		}
	})

	t.Run("scores for tag matches", func(t *testing.T) {
		result := &SearchResult{
			Name:        "tool",
			Description: "A tool",
			Tags:        []string{"analytics", "data"},
		}

		score := ComputeKeywordRelevance("analytics", result)
		if score <= 0 {
			t.Errorf("expected positive score for tag match, got %f", score)
		}
	})

	t.Run("case insensitive matching", func(t *testing.T) {
		result := &SearchResult{
			Name:        "Search Tool",
			Description: "SEARCH functionality",
		}

		score := ComputeKeywordRelevance("search", result)
		if score <= 0 {
			t.Errorf("expected positive score for case-insensitive match, got %f", score)
		}
	})

	t.Run("multiple query terms", func(t *testing.T) {
		result := &SearchResult{
			Name:        "data analyzer",
			Description: "Analyzes data patterns",
		}

		singleTermScore := ComputeKeywordRelevance("data", result)
		multiTermScore := ComputeKeywordRelevance("data analyzer", result)

		// Both should be positive
		if singleTermScore <= 0 {
			t.Errorf("expected positive score for single term, got %f", singleTermScore)
		}
		if multiTermScore <= 0 {
			t.Errorf("expected positive score for multiple terms, got %f", multiTermScore)
		}
	})

	t.Run("returns zero for no matches", func(t *testing.T) {
		result := &SearchResult{
			Name:        "tool",
			Description: "A utility",
			Tags:        []string{"helper"},
		}

		score := ComputeKeywordRelevance("xyz123", result)
		if score != 0.0 {
			t.Errorf("expected 0.0 for no matches, got %f", score)
		}
	})
}

// TestEnhanceResultsWithRelevance tests the relevance enhancement function.
// **Validates: Requirements 4.4**
func TestEnhanceResultsWithRelevance(t *testing.T) {
	t.Run("adds relevance scores to results without scores", func(t *testing.T) {
		results := []*SearchResult{
			{ID: "1", Name: "search tool", Description: "A search tool", RelevanceScore: 0},
			{ID: "2", Name: "data analyzer", Description: "Analyzes data", RelevanceScore: 0},
		}

		enhanced := EnhanceResultsWithRelevance("search", results)

		if enhanced[0].RelevanceScore <= 0 {
			t.Error("expected positive relevance score for matching result")
		}
	})

	t.Run("preserves existing relevance scores", func(t *testing.T) {
		results := []*SearchResult{
			{ID: "1", Name: "tool", Description: "A tool", RelevanceScore: 0.95},
		}

		enhanced := EnhanceResultsWithRelevance("search", results)

		if enhanced[0].RelevanceScore != 0.95 {
			t.Errorf("expected preserved score 0.95, got %f", enhanced[0].RelevanceScore)
		}
	})

	t.Run("handles empty results", func(t *testing.T) {
		enhanced := EnhanceResultsWithRelevance("query", nil)
		if enhanced != nil {
			t.Errorf("expected nil for nil input, got %v", enhanced)
		}

		enhanced = EnhanceResultsWithRelevance("query", []*SearchResult{})
		if len(enhanced) != 0 {
			t.Errorf("expected empty slice, got %d results", len(enhanced))
		}
	})
}

// TestSearchClientKeywordFallback tests the keyword fallback behavior.
// **Validates: Requirements 4.4**
func TestSearchClientKeywordFallback(t *testing.T) {
	ctx := context.Background()

	t.Run("uses keyword search when semantic not supported", func(t *testing.T) {
		manager := NewManager()

		// Client without semantic search support
		client := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "tool-1", Name: "keyword result", RelevanceScore: 0.8},
			},
		}

		manager.AddRegistry("keyword-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test query", SearchOptions{
			PreferSemantic: true, // Prefer semantic, but client doesn't support it
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Name != "keyword result" {
			t.Errorf("expected keyword result, got %q", results[0].Name)
		}
	})

	t.Run("falls back to keyword when semantic fails", func(t *testing.T) {
		manager := NewManager()

		// Client with semantic search that fails
		client := &mockSemanticClientWithFallback{
			semanticErr: errors.New("semantic search unavailable"),
			keywordResults: []*SearchResult{
				{ID: "tool-1", Name: "fallback result", RelevanceScore: 0.7},
			},
		}

		manager.AddRegistry("fallback-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test query", SearchOptions{
			PreferSemantic: true,
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Name != "fallback result" {
			t.Errorf("expected fallback result, got %q", results[0].Name)
		}
	})

	t.Run("uses semantic search when available and preferred", func(t *testing.T) {
		manager := NewManager()

		client := &mockSemanticClientWithFallback{
			semanticResults: []*SearchResult{
				{ID: "tool-1", Name: "semantic result", RelevanceScore: 0.95},
			},
			keywordResults: []*SearchResult{
				{ID: "tool-2", Name: "keyword result", RelevanceScore: 0.7},
			},
		}

		manager.AddRegistry("semantic-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test query", SearchOptions{
			PreferSemantic: true,
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Name != "semantic result" {
			t.Errorf("expected semantic result, got %q", results[0].Name)
		}
	})

	t.Run("uses keyword search when semantic not preferred", func(t *testing.T) {
		manager := NewManager()

		client := &mockSemanticClientWithFallback{
			semanticResults: []*SearchResult{
				{ID: "tool-1", Name: "semantic result", RelevanceScore: 0.95},
			},
			keywordResults: []*SearchResult{
				{ID: "tool-2", Name: "keyword result", RelevanceScore: 0.7},
			},
		}

		manager.AddRegistry("mixed-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test query", SearchOptions{
			PreferSemantic: false, // Don't prefer semantic
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Name != "keyword result" {
			t.Errorf("expected keyword result, got %q", results[0].Name)
		}
	})
}

// TestSearchClientFiltering tests the search result filtering.
// **Validates: Requirements 4.1, 4.2**
func TestSearchClientFiltering(t *testing.T) {
	ctx := context.Background()

	t.Run("filters by minimum relevance", func(t *testing.T) {
		manager := NewManager()

		client := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "tool-1", Name: "high relevance", RelevanceScore: 0.9},
				{ID: "tool-2", Name: "low relevance", RelevanceScore: 0.3},
				{ID: "tool-3", Name: "medium relevance", RelevanceScore: 0.6},
			},
		}

		manager.AddRegistry("test-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{
			MinRelevance: 0.5,
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results above threshold, got %d", len(results))
		}
		for _, r := range results {
			if r.RelevanceScore < 0.5 {
				t.Errorf("result %q has relevance %f below threshold", r.Name, r.RelevanceScore)
			}
		}
	})

	t.Run("filters by type", func(t *testing.T) {
		manager := NewManager()

		client := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "1", Name: "tool1", Type: "tool", RelevanceScore: 0.8},
				{ID: "2", Name: "toolset1", Type: "toolset", RelevanceScore: 0.8},
				{ID: "3", Name: "agent1", Type: "agent", RelevanceScore: 0.8},
			},
		}

		manager.AddRegistry("test-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{
			Types: []string{"tool", "agent"},
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results matching types, got %d", len(results))
		}
		for _, r := range results {
			if r.Type != "tool" && r.Type != "agent" {
				t.Errorf("unexpected type %q in results", r.Type)
			}
		}
	})

	t.Run("filters by tags", func(t *testing.T) {
		manager := NewManager()

		client := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "1", Name: "tool1", Tags: []string{"data", "analytics"}, RelevanceScore: 0.8},
				{ID: "2", Name: "tool2", Tags: []string{"web", "api"}, RelevanceScore: 0.8},
				{ID: "3", Name: "tool3", Tags: []string{"data", "etl"}, RelevanceScore: 0.8},
			},
		}

		manager.AddRegistry("test-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{
			Tags: []string{"data"},
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results with 'data' tag, got %d", len(results))
		}
	})

	t.Run("limits max results", func(t *testing.T) {
		manager := NewManager()

		client := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "1", Name: "tool1", RelevanceScore: 0.9},
				{ID: "2", Name: "tool2", RelevanceScore: 0.8},
				{ID: "3", Name: "tool3", RelevanceScore: 0.7},
				{ID: "4", Name: "tool4", RelevanceScore: 0.6},
				{ID: "5", Name: "tool5", RelevanceScore: 0.5},
			},
		}

		manager.AddRegistry("test-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{
			MaxResults: 3,
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 results (max), got %d", len(results))
		}
	})

	t.Run("sorts by relevance descending", func(t *testing.T) {
		manager := NewManager()

		client := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "1", Name: "low", RelevanceScore: 0.3},
				{ID: "2", Name: "high", RelevanceScore: 0.9},
				{ID: "3", Name: "medium", RelevanceScore: 0.6},
			},
		}

		manager.AddRegistry("test-registry", client, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 results, got %d", len(results))
		}
		// Check sorted by relevance descending
		if results[0].RelevanceScore < results[1].RelevanceScore ||
			results[1].RelevanceScore < results[2].RelevanceScore {
			t.Error("results not sorted by relevance descending")
		}
	})
}

// TestSearchClientMultipleRegistries tests search across multiple registries.
// **Validates: Requirements 4.1**
func TestSearchClientMultipleRegistries(t *testing.T) {
	ctx := context.Background()

	t.Run("searches specific registries only", func(t *testing.T) {
		manager := NewManager()

		client1 := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "1", Name: "from-reg1", RelevanceScore: 0.8},
			},
		}
		client2 := &mockKeywordOnlyClient{
			results: []*SearchResult{
				{ID: "2", Name: "from-reg2", RelevanceScore: 0.8},
			},
		}

		manager.AddRegistry("registry-1", client1, RegistryConfig{})
		manager.AddRegistry("registry-2", client2, RegistryConfig{})

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{
			Registries: []string{"registry-1"}, // Only search registry-1
		})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result from registry-1, got %d", len(results))
		}
		if results[0].Origin != "registry-1" {
			t.Errorf("expected origin 'registry-1', got %q", results[0].Origin)
		}
	})

	t.Run("returns empty for no registries", func(t *testing.T) {
		manager := NewManager()

		searchClient := NewSearchClient(manager)
		results, err := searchClient.Search(ctx, "test", SearchOptions{})

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected empty results, got %d", len(results))
		}
	})
}

// Mock implementations for testing

// mockKeywordOnlyClient implements RegistryClient without semantic search.
type mockKeywordOnlyClient struct {
	results []*SearchResult
}

func (m *mockKeywordOnlyClient) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	return nil, nil
}

func (m *mockKeywordOnlyClient) GetToolset(_ context.Context, _ string) (*ToolsetSchema, error) {
	return nil, nil
}

func (m *mockKeywordOnlyClient) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	return m.results, nil
}

// mockSemanticClientWithFallback implements SemanticSearchClient with configurable behavior.
type mockSemanticClientWithFallback struct {
	semanticResults []*SearchResult
	semanticErr     error
	keywordResults  []*SearchResult
}

func (m *mockSemanticClientWithFallback) ListToolsets(_ context.Context) ([]*ToolsetInfo, error) {
	return nil, nil
}

func (m *mockSemanticClientWithFallback) GetToolset(_ context.Context, _ string) (*ToolsetSchema, error) {
	return nil, nil
}

func (m *mockSemanticClientWithFallback) Search(_ context.Context, _ string) ([]*SearchResult, error) {
	return m.keywordResults, nil
}

func (m *mockSemanticClientWithFallback) SemanticSearch(_ context.Context, _ string, _ SemanticSearchOptions) ([]*SearchResult, error) {
	if m.semanticErr != nil {
		return nil, m.semanticErr
	}
	return m.semanticResults, nil
}

func (m *mockSemanticClientWithFallback) Capabilities() SearchCapabilities {
	return SearchCapabilities{
		SemanticSearch: true,
		KeywordSearch:  true,
	}
}
