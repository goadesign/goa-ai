// Package prompt provides an in-memory implementation of the prompt.Store interface
// for tests and local development.
package prompt

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"
)

type (
	// InMemoryStore stores prompt overrides in process memory.
	//
	// This implementation is primarily intended for local development and tests.
	// Overrides are not persisted across process restarts.
	InMemoryStore struct {
		mu        sync.RWMutex
		overrides []*Override
	}
)

// NewInMemoryStore returns an empty in-memory prompt store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		overrides: make([]*Override, 0),
	}
}

// Resolve returns the highest-precedence override for promptID and scope.
//
// Precedence order is:
//   - session-scoped overrides before non-session overrides
//   - then most constrained label set (more labels = more specific)
//   - then newest override (CreatedAt timestamp)
//   - deterministic tie-breaker on equal CreatedAt: lexicographical comparison
//     of sorted label key and value pairs
func (s *InMemoryStore) Resolve(ctx context.Context, promptID Ident, scope Scope) (*Override, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if promptID == "" {
		return nil, fmt.Errorf("resolve prompt override: promptID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var best *Override
	for _, override := range s.overrides {
		if override.PromptID != promptID {
			continue
		}
		if !ScopeMatches(override.Scope, scope) {
			continue
		}
		if OverridePrecedes(override, best) {
			best = override
		}
	}
	if best == nil {
		return nil, nil
	}
	return cloneOverride(best), nil
}

// Set persists one override record.
func (s *InMemoryStore) Set(ctx context.Context, promptID Ident, scope Scope, template string, metadata map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if promptID == "" {
		return fmt.Errorf("set prompt override: promptID is required")
	}
	if template == "" {
		return fmt.Errorf("set prompt override: template is required")
	}

	override := &Override{
		PromptID:  promptID,
		Scope:     cloneScope(scope),
		Template:  template,
		Version:   VersionFromTemplate(template),
		CreatedAt: time.Now().UTC(),
		Metadata:  cloneMetadata(metadata),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides = append(s.overrides, override)
	return nil
}

// Reset destructively removes stored override records matching the exact
// (promptID, scope) pair. Subsequent resolutions for this promptID and scope
// fall back to the next applicable scoped override or baseline spec.
// Matching on scope is exact: SessionID must match exactly, and the labels map
// must contain identical key/value entries with no extra or missing keys.
func (s *InMemoryStore) Reset(ctx context.Context, promptID Ident, scope Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if promptID == "" {
		return fmt.Errorf("reset prompt override: promptID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := make([]*Override, 0, len(s.overrides))
	for _, override := range s.overrides {
		if override.PromptID == promptID && exactScopeMatch(override.Scope, scope) {
			continue
		}
		filtered = append(filtered, override)
	}
	s.overrides = filtered
	return nil
}

// History returns all stored override records for promptID, newest-first.
func (s *InMemoryStore) History(ctx context.Context, promptID Ident) ([]*Override, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if promptID == "" {
		return nil, fmt.Errorf("prompt history: promptID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	history := make([]*Override, 0)
	for _, override := range s.overrides {
		if override.PromptID != promptID {
			continue
		}
		history = append(history, cloneOverride(override))
	}
	sort.Slice(history, func(i, j int) bool {
		return history[i].CreatedAt.After(history[j].CreatedAt)
	})
	return history, nil
}

// List returns all overrides, newest-first.
func (s *InMemoryStore) List(ctx context.Context) ([]*Override, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	overrides := make([]*Override, 0, len(s.overrides))
	for _, override := range s.overrides {
		overrides = append(overrides, cloneOverride(override))
	}
	sort.Slice(overrides, func(i, j int) bool {
		return overrides[i].CreatedAt.After(overrides[j].CreatedAt)
	})
	return overrides, nil
}

// exactScopeMatch returns true if two scopes have identical SessionID and identical Labels.
func exactScopeMatch(a, b Scope) bool {
	if a.SessionID != b.SessionID {
		return false
	}
	if len(a.Labels) == 0 && len(b.Labels) == 0 {
		return true
	}
	return maps.Equal(a.Labels, b.Labels)
}

// cloneOverride returns a deep copy safe for callers to mutate.
func cloneOverride(override *Override) *Override {
	if override == nil {
		return nil
	}
	return &Override{
		PromptID:  override.PromptID,
		Scope:     cloneScope(override.Scope),
		Template:  override.Template,
		Version:   override.Version,
		CreatedAt: override.CreatedAt,
		Metadata:  cloneMetadata(override.Metadata),
	}
}

func cloneScope(scope Scope) Scope {
	return Scope{
		SessionID: scope.SessionID,
		Labels:    cloneMetadata(scope.Labels),
	}
}

// cloneMetadata copies metadata maps and preserves nil.
func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
