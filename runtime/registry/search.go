// Package registry provides runtime components for managing MCP registry
// connections, tool discovery, and catalog synchronization.
package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type (
	// SearchClient provides search functionality across registries with
	// semantic search and keyword fallback support.
	SearchClient struct {
		manager *Manager
		obs     *Observability
	}

	// SearchOptions configures search behavior.
	SearchOptions struct {
		// Registries limits search to specific registries. If empty, all registries are searched.
		Registries []string
		// Types filters results by type (e.g., "tool", "toolset", "agent").
		Types []string
		// Tags filters results by tags.
		Tags []string
		// MinRelevance filters results below this relevance score (0.0-1.0).
		MinRelevance float64
		// MaxResults limits the number of results returned.
		MaxResults int
		// PreferSemantic indicates whether to prefer semantic search over keyword.
		// When true, keyword search is only used as fallback.
		PreferSemantic bool
	}

	// SearchCapabilities describes what search features a registry supports.
	SearchCapabilities struct {
		// SemanticSearch indicates if the registry supports semantic/vector search.
		SemanticSearch bool
		// KeywordSearch indicates if the registry supports keyword-based search.
		KeywordSearch bool
		// TagFiltering indicates if the registry supports filtering by tags.
		TagFiltering bool
		// TypeFiltering indicates if the registry supports filtering by type.
		TypeFiltering bool
	}

	// SemanticSearchClient extends RegistryClient with semantic search capabilities.
	SemanticSearchClient interface {
		RegistryClient
		// SemanticSearch performs a semantic/vector search on the registry.
		SemanticSearch(ctx context.Context, query string, opts SemanticSearchOptions) ([]*SearchResult, error)
		// Capabilities returns the search capabilities of this registry.
		Capabilities() SearchCapabilities
	}

	// SemanticSearchOptions configures semantic search behavior.
	SemanticSearchOptions struct {
		// Types filters results by type.
		Types []string
		// Tags filters results by tags.
		Tags []string
		// MaxResults limits the number of results.
		MaxResults int
	}
)

// NewSearchClient creates a new search client using the given manager.
func NewSearchClient(manager *Manager) *SearchClient {
	return &SearchClient{
		manager: manager,
		obs:     manager.obs,
	}
}

// Search performs a search across registries with automatic fallback.
// If semantic search is preferred and available, it is used first.
// If semantic search fails or is not supported, keyword search is used as fallback.
func (s *SearchClient) Search(ctx context.Context, query string, opts SearchOptions) ([]*SearchResult, error) {
	start := time.Now()

	// Start trace span
	ctx, span := s.obs.StartSpan(ctx, OpSearch,
		attribute.String("query", query),
		attribute.Bool("prefer_semantic", opts.PreferSemantic),
	)

	var outcome OperationOutcome
	var opErr error
	var resultCount int
	defer func() {
		duration := time.Since(start)
		event := OperationEvent{
			Operation:   OpSearch,
			Query:       query,
			Duration:    duration,
			Outcome:     outcome,
			ResultCount: resultCount,
		}
		if opErr != nil {
			event.Error = opErr.Error()
		}
		s.obs.LogOperation(ctx, event)
		s.obs.RecordOperationMetrics(event)
		s.obs.EndSpan(span, outcome, opErr)
	}()

	// Get registries to search
	entries := s.getRegistriesToSearch(opts.Registries)
	if len(entries) == 0 {
		outcome = OutcomeSuccess
		return nil, nil
	}

	span.AddEvent("searching_registries", "registry_count", len(entries))

	// Perform search across all registries
	var allResults []*SearchResult
	var searchErrors []error

	for name, entry := range entries {
		results, err := s.searchRegistry(ctx, name, entry, query, opts)
		if err != nil {
			searchErrors = append(searchErrors, fmt.Errorf("registry %q: %w", name, err))
			continue
		}
		allResults = append(allResults, results...)
	}

	// Return error only if all registries failed
	if len(searchErrors) == len(entries) && len(searchErrors) > 0 {
		outcome = OutcomeError
		opErr = fmt.Errorf("all registries failed: %v", searchErrors)
		return nil, opErr
	}

	// Apply post-processing filters
	allResults = s.filterResults(allResults, opts)

	// Sort by relevance score (descending)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].RelevanceScore > allResults[j].RelevanceScore
	})

	// Apply max results limit
	if opts.MaxResults > 0 && len(allResults) > opts.MaxResults {
		allResults = allResults[:opts.MaxResults]
	}

	resultCount = len(allResults)
	outcome = OutcomeSuccess
	return allResults, nil
}

// getRegistriesToSearch returns the registry entries to search based on options.
func (s *SearchClient) getRegistriesToSearch(registries []string) map[string]*registryEntry {
	s.manager.mu.RLock()
	defer s.manager.mu.RUnlock()

	if len(registries) == 0 {
		// Search all registries
		entries := make(map[string]*registryEntry, len(s.manager.registries))
		for name, entry := range s.manager.registries {
			entries[name] = entry
		}
		return entries
	}

	// Search only specified registries
	entries := make(map[string]*registryEntry, len(registries))
	for _, name := range registries {
		if entry, ok := s.manager.registries[name]; ok {
			entries[name] = entry
		}
	}
	return entries
}

// searchRegistry performs a search on a single registry with fallback logic.
func (s *SearchClient) searchRegistry(ctx context.Context, name string, entry *registryEntry, query string, opts SearchOptions) ([]*SearchResult, error) {
	// Check if client supports semantic search
	semanticClient, hasSemanticSearch := entry.client.(SemanticSearchClient)

	if opts.PreferSemantic && hasSemanticSearch {
		caps := semanticClient.Capabilities()
		if caps.SemanticSearch {
			// Try semantic search first
			semanticOpts := SemanticSearchOptions{
				Types:      opts.Types,
				Tags:       opts.Tags,
				MaxResults: opts.MaxResults,
			}
			results, err := semanticClient.SemanticSearch(ctx, query, semanticOpts)
			if err == nil {
				// Tag results with origin
				for _, r := range results {
					if r.Origin == "" {
						r.Origin = name
					}
				}
				return results, nil
			}
			// Semantic search failed, fall back to keyword search
			s.manager.logger.Warn(ctx, "semantic search failed, falling back to keyword search",
				"registry", name, "query", query, "error", err)
		}
	}

	// Use keyword search (either as primary or fallback)
	results, err := s.keywordSearch(ctx, entry.client, query)
	if err != nil {
		return nil, err
	}

	// Tag results with origin
	for _, r := range results {
		if r.Origin == "" {
			r.Origin = name
		}
	}

	return results, nil
}

// keywordSearch performs a keyword-based search using the registry's Search method.
// This is the fallback when semantic search is not available or fails.
func (s *SearchClient) keywordSearch(ctx context.Context, client RegistryClient, query string) ([]*SearchResult, error) {
	// Use the standard Search method which performs keyword search
	return client.Search(ctx, query)
}

// filterResults applies post-processing filters to search results.
func (s *SearchClient) filterResults(results []*SearchResult, opts SearchOptions) []*SearchResult {
	if len(results) == 0 {
		return results
	}

	filtered := make([]*SearchResult, 0, len(results))
	for _, r := range results {
		// Filter by minimum relevance
		if opts.MinRelevance > 0 && r.RelevanceScore < opts.MinRelevance {
			continue
		}

		// Filter by types
		if len(opts.Types) > 0 && !containsString(opts.Types, r.Type) {
			continue
		}

		// Filter by tags (result must have at least one matching tag)
		if len(opts.Tags) > 0 && !hasMatchingTag(r.Tags, opts.Tags) {
			continue
		}

		filtered = append(filtered, r)
	}

	return filtered
}

// containsString checks if a slice contains a string.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// hasMatchingTag checks if any tag in resultTags matches any tag in filterTags.
func hasMatchingTag(resultTags, filterTags []string) bool {
	for _, rt := range resultTags {
		for _, ft := range filterTags {
			if rt == ft {
				return true
			}
		}
	}
	return false
}

// ParseSearchResults parses search results from a registry response.
// This helper ensures all required fields are present and properly formatted.
// Results without an ID are skipped rather than causing an error.
func ParseSearchResults(results []*SearchResult) []*SearchResult {
	if len(results) == 0 {
		return results
	}

	parsed := make([]*SearchResult, 0, len(results))
	for _, r := range results {
		// Validate required fields per Requirements 4.2
		if r.ID == "" {
			continue // Skip results without ID
		}

		// Ensure all required fields are present
		result := &SearchResult{
			ID:             r.ID,
			Name:           r.Name,
			Description:    r.Description,
			Type:           r.Type,
			SchemaRef:      r.SchemaRef,
			RelevanceScore: r.RelevanceScore,
			Tags:           r.Tags,
			Origin:         r.Origin,
		}

		// Default type if not specified
		if result.Type == "" {
			result.Type = "tool"
		}

		parsed = append(parsed, result)
	}

	return parsed
}

// ComputeKeywordRelevance computes a simple relevance score for keyword search results.
// This is used when the registry doesn't provide relevance scores.
func ComputeKeywordRelevance(query string, result *SearchResult) float64 {
	if query == "" {
		return 0.0
	}

	queryLower := strings.ToLower(query)
	queryTerms := strings.Fields(queryLower)

	var score float64
	var maxScore float64

	// Check name (highest weight)
	nameLower := strings.ToLower(result.Name)
	for _, term := range queryTerms {
		maxScore += 3.0
		if strings.Contains(nameLower, term) {
			score += 3.0
		}
	}

	// Check description (medium weight)
	descLower := strings.ToLower(result.Description)
	for _, term := range queryTerms {
		maxScore += 2.0
		if strings.Contains(descLower, term) {
			score += 2.0
		}
	}

	// Check tags (lower weight)
	for _, tag := range result.Tags {
		tagLower := strings.ToLower(tag)
		for _, term := range queryTerms {
			maxScore += 1.0
			if strings.Contains(tagLower, term) {
				score += 1.0
			}
		}
	}

	if maxScore == 0 {
		return 0.0
	}

	return score / maxScore
}

// EnhanceResultsWithRelevance adds relevance scores to results that don't have them.
func EnhanceResultsWithRelevance(query string, results []*SearchResult) []*SearchResult {
	for _, r := range results {
		if r.RelevanceScore == 0 {
			r.RelevanceScore = ComputeKeywordRelevance(query, r)
		}
	}
	return results
}
