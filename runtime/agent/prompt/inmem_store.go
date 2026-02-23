package prompt

import (
	"context"
	"fmt"
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
//   - then newest override
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
	bestLevel := -1
	for _, override := range s.overrides {
		if override.PromptID != promptID {
			continue
		}
		if !ScopeMatches(override.Scope, scope) {
			continue
		}
		level := ScopePrecedence(override.Scope)
		if best == nil || level > bestLevel || (level == bestLevel && override.CreatedAt.After(best.CreatedAt)) {
			best = override
			bestLevel = level
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

// History returns override records for one prompt, newest-first.
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
