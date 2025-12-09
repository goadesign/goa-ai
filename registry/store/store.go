// Package store defines the persistence layer interface for the registry.
//
// The Store interface abstracts toolset metadata storage, allowing different
// backend implementations. Available implementations:
//
//   - memory: In-memory store for development and testing
//   - mongo: MongoDB store for production persistence
//
// To add a new implementation, create a subpackage that implements the Store
// interface and returns store.ErrNotFound for missing toolsets.
package store

import (
	"context"
	"errors"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// ErrNotFound is returned when a toolset is not found in the store.
var ErrNotFound = errors.New("toolset not found")

// Store defines the persistence layer for toolset metadata.
// Implementations must be safe for concurrent use.
type Store interface {
	// SaveToolset stores or updates a toolset. If a toolset with the same
	// name already exists, it is replaced.
	SaveToolset(ctx context.Context, toolset *genregistry.Toolset) error

	// GetToolset retrieves a toolset by name. Returns ErrNotFound if the
	// toolset does not exist.
	GetToolset(ctx context.Context, name string) (*genregistry.Toolset, error)

	// DeleteToolset removes a toolset by name. Returns ErrNotFound if the
	// toolset does not exist.
	DeleteToolset(ctx context.Context, name string) error

	// ListToolsets returns all toolsets, optionally filtered by tags.
	// If tags is non-empty, only toolsets containing all specified tags
	// are returned. Returns an empty slice if no toolsets match.
	ListToolsets(ctx context.Context, tags []string) ([]*genregistry.Toolset, error)

	// SearchToolsets searches toolsets by query string. The query is matched
	// against toolset name, description, and tags (case-insensitive).
	// Returns an empty slice if no toolsets match.
	SearchToolsets(ctx context.Context, query string) ([]*genregistry.Toolset, error)
}
