// Package replicated provides a replicated-map backed implementation of the
// registry store.
//
// The store persists toolset metadata in a Pulse replicated map (rmap), which is
// backed by Redis. This makes toolset registrations durable across registry
// process restarts and visible to all nodes in a multi-node registry cluster.
package replicated

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store"
)

type (
	// Map is the minimal replicated-map contract required by the replicated store.
	//
	// Map is satisfied by `*rmap.Map` from `goa.design/pulse/rmap`.
	// It is defined here to:
	//   - keep the replicated store unit-testable without Redis, and
	//   - avoid coupling callers to a concrete Pulse implementation.
	//
	// Implementations must be safe for concurrent use.
	Map interface {
		Delete(ctx context.Context, key string) (string, error)
		Get(key string) (string, bool)
		Keys() []string
		Set(ctx context.Context, key, value string) (string, error)
	}

	// Store persists toolset metadata in a replicated map.
	// It is safe for concurrent use when backed by a concurrent-safe map (such as rmap.Map).
	Store struct {
		m Map
	}
)

const toolsetKeyPrefix = "registry:toolset:"

// New creates a new replicated store backed by the given map.
func New(m Map) *Store {
	return &Store{m: m}
}

// Compile-time check that Store implements store.Store.
var _ store.Store = (*Store)(nil)

// SaveToolset stores or updates a toolset.
func (s *Store) SaveToolset(ctx context.Context, toolset *genregistry.Toolset) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(toolset)
	if err != nil {
		return fmt.Errorf("marshal toolset %q: %w", toolset.Name, err)
	}
	_, err = s.m.Set(ctx, toolsetKey(toolset.Name), string(b))
	if err != nil {
		return fmt.Errorf("store toolset %q: %w", toolset.Name, err)
	}
	return nil
}

// GetToolset retrieves a toolset by name.
func (s *Store) GetToolset(ctx context.Context, name string) (*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	val, ok := s.m.Get(toolsetKey(name))
	if !ok {
		return nil, store.ErrNotFound
	}
	var ts genregistry.Toolset
	if err := json.Unmarshal([]byte(val), &ts); err != nil {
		return nil, fmt.Errorf("unmarshal toolset %q: %w", name, err)
	}
	return &ts, nil
}

// DeleteToolset removes a toolset by name.
func (s *Store) DeleteToolset(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := toolsetKey(name)
	if _, ok := s.m.Get(key); !ok {
		return store.ErrNotFound
	}
	if _, err := s.m.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete toolset %q: %w", name, err)
	}
	return nil
}

// ListToolsets returns all toolsets, optionally filtered by tags.
func (s *Store) ListToolsets(ctx context.Context, tags []string) ([]*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := s.m.Keys()
	out := make([]*genregistry.Toolset, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, toolsetKeyPrefix) {
			continue
		}
		name := strings.TrimPrefix(k, toolsetKeyPrefix)
		ts, err := s.GetToolset(ctx, name)
		if err != nil {
			return nil, err
		}
		if matchesTags(ts.Tags, tags) {
			out = append(out, ts)
		}
	}
	return out, nil
}

// SearchToolsets searches toolsets by query string.
func (s *Store) SearchToolsets(ctx context.Context, query string) ([]*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	keys := s.m.Keys()
	out := make([]*genregistry.Toolset, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, toolsetKeyPrefix) {
			continue
		}
		name := strings.TrimPrefix(k, toolsetKeyPrefix)
		ts, err := s.GetToolset(ctx, name)
		if err != nil {
			return nil, err
		}
		if matchesQuery(ts, lowerQuery) {
			out = append(out, ts)
		}
	}
	return out, nil
}

func toolsetKey(name string) string {
	return toolsetKeyPrefix + name
}

func matchesTags(toolsetTags, filterTags []string) bool {
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

func matchesQuery(toolset *genregistry.Toolset, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(toolset.Name), lowerQuery) {
		return true
	}
	if toolset.Description != nil && strings.Contains(strings.ToLower(*toolset.Description), lowerQuery) {
		return true
	}
	for _, tag := range toolset.Tags {
		if strings.Contains(strings.ToLower(tag), lowerQuery) {
			return true
		}
	}
	return false
}
