// Package registry provides runtime components for managing MCP registry
// connections, tool discovery, and catalog synchronization.
package registry

import (
	"context"
	"fmt"
	"path"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	// Manager coordinates multiple registry clients, providing unified
	// discovery, search, and caching across all configured registries.
	Manager struct {
		mu         sync.RWMutex
		registries map[string]*registryEntry
		cache      Cache
		logger     telemetry.Logger
		metrics    telemetry.Metrics
		tracer     telemetry.Tracer
		obs        *Observability

		// Sync loop control
		syncCtx    context.Context
		syncCancel context.CancelFunc
		syncWg     sync.WaitGroup
	}

	// registryEntry holds a registry client and its configuration.
	registryEntry struct {
		client       RegistryClient
		syncInterval time.Duration
		cacheTTL     time.Duration
		federation   *FederationConfig
	}

	// FederationConfig holds federation settings for a registry.
	FederationConfig struct {
		// Include patterns for namespaces to import.
		Include []string
		// Exclude patterns for namespaces to skip.
		Exclude []string
	}

	// RegistryClient defines the interface for registry operations.
	// Generated registry clients implement this interface.
	RegistryClient interface {
		// ListToolsets returns all available toolsets from the registry.
		ListToolsets(ctx context.Context) ([]*ToolsetInfo, error)
		// GetToolset retrieves the full schema for a specific toolset.
		GetToolset(ctx context.Context, name string) (*ToolsetSchema, error)
		// Search performs a semantic or keyword search on the registry.
		Search(ctx context.Context, query string) ([]*SearchResult, error)
	}

	// ToolsetInfo contains metadata about a toolset available in a registry.
	ToolsetInfo struct {
		// ID is the unique identifier for the toolset.
		ID string
		// Name is the human-readable name.
		Name string
		// Description provides details about the toolset.
		Description string
		// Version is the toolset version.
		Version string
		// Tags are metadata tags for discovery.
		Tags []string
		// Origin indicates the source registry for federated items.
		Origin string
	}

	// ToolsetSchema contains the full schema for a toolset including its tools.
	ToolsetSchema struct {
		// ID is the unique identifier for the toolset.
		ID string
		// Name is the human-readable name.
		Name string
		// Description provides details about the toolset.
		Description string
		// Version is the toolset version.
		Version string
		// Tools contains the tool definitions.
		Tools []*ToolSchema
		// Origin indicates the source registry for federated items.
		Origin string
	}

	// ToolSchema contains the schema for a single tool.
	ToolSchema struct {
		// Name is the tool identifier.
		Name string
		// Description explains what the tool does.
		Description string
		// InputSchema is the JSON Schema for tool input.
		InputSchema []byte
	}

	// SearchResult contains a single search result from the registry.
	SearchResult struct {
		// ID is the unique identifier.
		ID string
		// Name is the human-readable name.
		Name string
		// Description provides details.
		Description string
		// Type indicates the result type (e.g., "tool", "toolset", "agent").
		Type string
		// SchemaRef is a reference to the full schema.
		SchemaRef string
		// RelevanceScore indicates how relevant this result is to the query.
		RelevanceScore float64
		// Tags are metadata tags.
		Tags []string
		// Origin indicates the federation source if applicable.
		Origin string
	}

	// Option configures a Manager.
	Option func(*Manager)
)

// WithCache sets the cache implementation for the manager.
func WithCache(c Cache) Option {
	return func(m *Manager) {
		m.cache = c
	}
}

// WithLogger sets the logger for the manager.
func WithLogger(l telemetry.Logger) Option {
	return func(m *Manager) {
		m.logger = l
	}
}

// WithMetrics sets the metrics recorder for the manager.
func WithMetrics(met telemetry.Metrics) Option {
	return func(m *Manager) {
		m.metrics = met
	}
}

// WithTracer sets the tracer for the manager.
func WithTracer(t telemetry.Tracer) Option {
	return func(m *Manager) {
		m.tracer = t
	}
}

// NewManager creates a new registry manager with the given options.
func NewManager(opts ...Option) *Manager {
	m := &Manager{
		registries: make(map[string]*registryEntry),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	// Use noop implementations if not provided
	if m.cache == nil {
		m.cache = &noopCache{}
	}
	if m.logger == nil {
		m.logger = &noopLogger{}
	}
	if m.metrics == nil {
		m.metrics = &noopMetrics{}
	}
	if m.tracer == nil {
		m.tracer = &noopTracer{}
	}
	// Create observability helper
	m.obs = NewObservability(m.logger, m.metrics, m.tracer)
	return m
}

// RegistryConfig holds configuration for adding a registry to the manager.
type RegistryConfig struct {
	// SyncInterval specifies how often to refresh the registry catalog.
	SyncInterval time.Duration
	// CacheTTL specifies local cache duration for registry data.
	CacheTTL time.Duration
	// Federation configures external registry import settings.
	Federation *FederationConfig
}

// AddRegistry registers a registry client with the manager.
func (m *Manager) AddRegistry(name string, client RegistryClient, cfg RegistryConfig) {
	ctx := context.Background()
	start := time.Now()

	m.mu.Lock()
	m.registries[name] = &registryEntry{
		client:       client,
		syncInterval: cfg.SyncInterval,
		cacheTTL:     cfg.CacheTTL,
		federation:   cfg.Federation,
	}
	m.mu.Unlock()

	event := OperationEvent{
		Operation: OpRegister,
		Registry:  name,
		Duration:  time.Since(start),
		Outcome:   OutcomeSuccess,
	}
	m.obs.LogOperation(ctx, event)
	m.obs.RecordOperationMetrics(event)
}

// DiscoverToolset retrieves a toolset schema from the specified registry.
// It first checks the cache, then falls back to the registry client.
func (m *Manager) DiscoverToolset(ctx context.Context, registry, toolset string) (*ToolsetSchema, error) {
	start := time.Now()

	// Start trace span
	ctx, span := m.obs.StartSpan(ctx, OpDiscoverToolset,
		attribute.String("registry", registry),
		attribute.String("toolset", toolset),
	)

	var outcome OperationOutcome
	var opErr error
	defer func() {
		duration := time.Since(start)
		event := OperationEvent{
			Operation: OpDiscoverToolset,
			Registry:  registry,
			Toolset:   toolset,
			Duration:  duration,
			Outcome:   outcome,
			CacheKey:  cacheKey(registry, toolset),
		}
		if opErr != nil {
			event.Error = opErr.Error()
		}
		m.obs.LogOperation(ctx, event)
		m.obs.RecordOperationMetrics(event)
		m.obs.EndSpan(span, outcome, opErr)
	}()

	m.mu.RLock()
	entry, ok := m.registries[registry]
	m.mu.RUnlock()

	if !ok {
		outcome = OutcomeError
		opErr = fmt.Errorf("registry %q not found", registry)
		return nil, opErr
	}

	// Check cache first
	key := cacheKey(registry, toolset)
	if schema, err := m.cache.Get(ctx, key); err == nil && schema != nil {
		span.AddEvent("cache_hit", "cache_key", key)
		outcome = OutcomeCacheHit
		return schema, nil
	}
	span.AddEvent("cache_miss", "cache_key", key)

	// Fetch from registry
	schema, err := entry.client.GetToolset(ctx, toolset)
	if err != nil {
		// Try to return cached data on error (fallback)
		if cached, cacheErr := m.cache.Get(ctx, key); cacheErr == nil && cached != nil {
			span.AddEvent("fallback_to_cache", "cache_key", key)
			outcome = OutcomeFallback
			return cached, nil
		}
		outcome = OutcomeError
		opErr = fmt.Errorf("fetching toolset %q from registry %q: %w", toolset, registry, err)
		return nil, opErr
	}

	// Tag with origin
	schema.Origin = registry

	// Cache the result
	ttl := entry.cacheTTL
	if ttl == 0 {
		ttl = time.Hour // Default TTL
	}
	if err := m.cache.Set(ctx, key, schema, ttl); err != nil {
		m.logger.Warn(ctx, "failed to cache toolset",
			"registry", registry, "toolset", toolset, "error", err)
	}

	outcome = OutcomeSuccess
	return schema, nil
}

// Search performs a search across all registries and merges results.
// Results are tagged with their origin registry.
func (m *Manager) Search(ctx context.Context, query string) ([]*SearchResult, error) {
	start := time.Now()

	// Start trace span
	ctx, span := m.obs.StartSpan(ctx, OpSearch,
		attribute.String("query", query),
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
		m.obs.LogOperation(ctx, event)
		m.obs.RecordOperationMetrics(event)
		m.obs.EndSpan(span, outcome, opErr)
	}()

	m.mu.RLock()
	entries := make(map[string]*registryEntry, len(m.registries))
	for name, entry := range m.registries {
		entries[name] = entry
	}
	m.mu.RUnlock()

	if len(entries) == 0 {
		outcome = OutcomeSuccess
		return nil, nil
	}

	span.AddEvent("searching_registries", "registry_count", len(entries))

	// Search all registries concurrently
	type searchResult struct {
		registry string
		results  []*SearchResult
		err      error
	}

	resultCh := make(chan searchResult, len(entries))
	var wg sync.WaitGroup

	for name, entry := range entries {
		wg.Add(1)
		go func(name string, entry *registryEntry) {
			defer wg.Done()

			results, err := entry.client.Search(ctx, query)
			if err != nil {
				m.logger.Warn(ctx, "search failed for registry",
					"registry", name, "query", query, "error", err)
				resultCh <- searchResult{registry: name, err: err}
				return
			}

			// Tag results with origin
			for _, r := range results {
				if r.Origin == "" {
					r.Origin = name
				}
			}

			resultCh <- searchResult{registry: name, results: results}
		}(name, entry)
	}

	// Wait for all searches to complete
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Merge results
	var merged []*SearchResult
	var errors []error

	for res := range resultCh {
		if res.err != nil {
			errors = append(errors, fmt.Errorf("registry %q: %w", res.registry, res.err))
			continue
		}
		merged = append(merged, res.results...)
	}

	resultCount = len(merged)

	// Log if some registries failed but we have results
	if len(errors) > 0 && len(merged) > 0 {
		span.AddEvent("partial_failure",
			"error_count", len(errors),
			"result_count", len(merged),
		)
	}

	// Return error only if all registries failed
	if len(errors) == len(entries) && len(errors) > 0 {
		outcome = OutcomeError
		opErr = fmt.Errorf("all registries failed: %v", errors)
		return nil, opErr
	}

	outcome = OutcomeSuccess
	return merged, nil
}

// cacheKey generates a cache key for a toolset.
func cacheKey(registry, toolset string) string {
	return path.Join("registry", registry, "toolset", toolset)
}

// noopLogger is a no-op logger implementation.
type noopLogger struct{}

func (noopLogger) Debug(context.Context, string, ...any) {}
func (noopLogger) Info(context.Context, string, ...any)  {}
func (noopLogger) Warn(context.Context, string, ...any)  {}
func (noopLogger) Error(context.Context, string, ...any) {}

// noopMetrics is a no-op metrics implementation.
type noopMetrics struct{}

func (noopMetrics) IncCounter(string, float64, ...string)        {}
func (noopMetrics) RecordTimer(string, time.Duration, ...string) {}
func (noopMetrics) RecordGauge(string, float64, ...string)       {}

// noopCache is a no-op cache implementation.
type noopCache struct{}

func (noopCache) Get(context.Context, string) (*ToolsetSchema, error) {
	return nil, nil
}

func (noopCache) Set(context.Context, string, *ToolsetSchema, time.Duration) error {
	return nil
}

func (noopCache) Delete(context.Context, string) error {
	return nil
}

// StartSync starts the background sync loop for all registries.
// Each registry is synced at its configured SyncInterval.
func (m *Manager) StartSync(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.syncCancel != nil {
		return fmt.Errorf("sync loop already running")
	}

	m.syncCtx, m.syncCancel = context.WithCancel(ctx)

	for name, entry := range m.registries {
		if entry.syncInterval <= 0 {
			continue
		}
		m.syncWg.Add(1)
		go m.syncRegistry(name, entry)
	}

	m.logger.Info(ctx, "sync loop started")
	return nil
}

// StopSync stops the background sync loop.
func (m *Manager) StopSync() {
	m.mu.Lock()
	cancel := m.syncCancel
	m.syncCancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.syncWg.Wait()

	m.logger.Info(context.Background(), "sync loop stopped")
}

// syncRegistry runs the sync loop for a single registry.
func (m *Manager) syncRegistry(name string, entry *registryEntry) {
	defer m.syncWg.Done()

	ticker := time.NewTicker(entry.syncInterval)
	defer ticker.Stop()

	// Initial sync
	m.doSync(name, entry)

	for {
		select {
		case <-m.syncCtx.Done():
			return
		case <-ticker.C:
			m.doSync(name, entry)
		}
	}
}

// doSync performs a single sync operation for a registry.
func (m *Manager) doSync(name string, entry *registryEntry) {
	ctx := m.syncCtx
	start := time.Now()

	// Start trace span
	ctx, span := m.obs.StartSpan(ctx, OpSync,
		attribute.String("registry", name),
	)

	var outcome OperationOutcome
	var opErr error
	var toolsetCount int
	defer func() {
		duration := time.Since(start)
		event := OperationEvent{
			Operation:   OpSync,
			Registry:    name,
			Duration:    duration,
			Outcome:     outcome,
			ResultCount: toolsetCount,
		}
		if opErr != nil {
			event.Error = opErr.Error()
		}
		m.obs.LogOperation(ctx, event)
		m.obs.RecordOperationMetrics(event)
		m.obs.EndSpan(span, outcome, opErr)
	}()

	toolsets, err := entry.client.ListToolsets(ctx)
	if err != nil {
		outcome = OutcomeError
		opErr = err
		return
	}

	// Apply federation filtering if configured
	if entry.federation != nil {
		originalCount := len(toolsets)
		toolsets = m.filterFederated(toolsets, entry.federation)
		span.AddEvent("federation_filter_applied",
			"original_count", originalCount,
			"filtered_count", len(toolsets),
		)
	}

	toolsetCount = len(toolsets)

	// Tag with origin and cache each toolset
	for _, ts := range toolsets {
		ts.Origin = name

		// Fetch full schema for caching
		schema, err := entry.client.GetToolset(ctx, ts.Name)
		if err != nil {
			m.logger.Warn(ctx, "failed to fetch toolset during sync",
				"registry", name, "toolset", ts.Name, "error", err)
			continue
		}
		schema.Origin = name

		ttl := entry.cacheTTL
		if ttl == 0 {
			ttl = time.Hour
		}
		key := cacheKey(name, ts.Name)
		if err := m.cache.Set(ctx, key, schema, ttl); err != nil {
			m.logger.Warn(ctx, "failed to cache toolset during sync",
				"registry", name, "toolset", ts.Name, "error", err)
		}
	}

	outcome = OutcomeSuccess
}

// filterFederated applies Include/Exclude patterns to filter toolsets.
// Include patterns whitelist namespaces; Exclude patterns blacklist them.
// If Include is empty, all namespaces are included by default.
func (m *Manager) filterFederated(toolsets []*ToolsetInfo, cfg *FederationConfig) []*ToolsetInfo {
	if cfg == nil {
		return toolsets
	}

	var filtered []*ToolsetInfo
	for _, ts := range toolsets {
		if m.shouldInclude(ts.Name, cfg) {
			filtered = append(filtered, ts)
		}
	}
	return filtered
}

// shouldInclude determines if a toolset should be included based on federation config.
func (m *Manager) shouldInclude(name string, cfg *FederationConfig) bool {
	// Check exclude patterns first
	for _, pattern := range cfg.Exclude {
		if matchGlob(pattern, name) {
			return false
		}
	}

	// If no include patterns, include everything not excluded
	if len(cfg.Include) == 0 {
		return true
	}

	// Check include patterns
	for _, pattern := range cfg.Include {
		if matchGlob(pattern, name) {
			return true
		}
	}

	return false
}

// matchGlob performs simple glob matching supporting * and ** wildcards.
// * matches any sequence of non-separator characters.
// ** matches any sequence including separators.
func matchGlob(pattern, name string) bool {
	// Handle exact match
	if pattern == name {
		return true
	}

	// Handle ** (match everything)
	if pattern == "**" {
		return true
	}

	// Handle trailing /* (match direct children)
	if len(pattern) > 2 && pattern[len(pattern)-2:] == "/*" {
		prefix := pattern[:len(pattern)-2]
		// Check if name starts with prefix and has no more slashes
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			rest := name[len(prefix)+1:]
			for i := 0; i < len(rest); i++ {
				if rest[i] == '/' {
					return false
				}
			}
			return true
		}
		return false
	}

	// Handle trailing /** (match all descendants)
	if len(pattern) > 3 && pattern[len(pattern)-3:] == "/**" {
		prefix := pattern[:len(pattern)-3]
		return len(name) >= len(prefix) && name[:len(prefix)] == prefix
	}

	// Handle prefix/* pattern
	if len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(name) >= len(prefix) && name[:len(prefix)] == prefix
	}

	return false
}
