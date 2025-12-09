// Package memory provides an in-memory implementation of the registry store.
//
// This implementation is suitable for development, testing, and single-node
// deployments where persistence across restarts is not required.
package memory

import (
	"context"
	"strings"
	"sync"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store"
)

// Store is an in-memory implementation of the store.Store interface.
// It is safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	toolsets map[string]*genregistry.Toolset
}

// Compile-time check that Store implements store.Store.
var _ store.Store = (*Store)(nil)

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		toolsets: make(map[string]*genregistry.Toolset),
	}
}

// SaveToolset stores or updates a toolset.
func (s *Store) SaveToolset(ctx context.Context, toolset *genregistry.Toolset) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolsets[toolset.Name] = toolset
	return nil
}

// GetToolset retrieves a toolset by name.
func (s *Store) GetToolset(ctx context.Context, name string) (*genregistry.Toolset, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	toolset, ok := s.toolsets[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return toolset, nil
}

// DeleteToolset removes a toolset by name.
func (s *Store) DeleteToolset(ctx context.Context, name string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.toolsets[name]; !ok {
		return store.ErrNotFound
	}
	delete(s.toolsets, name)
	return nil
}

// ListToolsets returns all toolsets, optionally filtered by tags.
func (s *Store) ListToolsets(ctx context.Context, tags []string) ([]*genregistry.Toolset, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*genregistry.Toolset, 0, len(s.toolsets))
	for _, toolset := range s.toolsets {
		if matchesTags(toolset.Tags, tags) {
			result = append(result, toolset)
		}
	}
	return result, nil
}

// SearchToolsets searches toolsets by query string.
func (s *Store) SearchToolsets(ctx context.Context, query string) ([]*genregistry.Toolset, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	lowerQuery := strings.ToLower(query)
	result := make([]*genregistry.Toolset, 0)
	for _, toolset := range s.toolsets {
		if matchesQuery(toolset, lowerQuery) {
			result = append(result, toolset)
		}
	}
	return result, nil
}

// matchesTags returns true if the toolset has all the specified tags.
// If tags is empty, returns true.
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

// matchesQuery returns true if the query matches the toolset's name,
// description, or any tag (case-insensitive).
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
