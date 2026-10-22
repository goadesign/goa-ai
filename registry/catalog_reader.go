// Package registry supports local inspection of saved catalog entries without
// starting a Registry server. CatalogReader borrows its Redis connection and
// uses the same saved-record checks as Service without running background work.
package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// CatalogReader reads saved registrations without changing Redis data, starting
// workers, or observing provider health. The caller owns authorization and the
// Redis client's lifetime. Returned declarations belong to each caller.
type CatalogReader struct {
	catalog *toolsetCatalog
}

// NewCatalogReader creates a reader for the named Registry without contacting
// Redis. An empty name selects "registry", matching New. The reader borrows
// client and requires no Close call; the caller remains responsible for client.
func NewCatalogReader(client *redis.Client, name string) (*CatalogReader, error) {
	if client == nil {
		return nil, errors.New("redis client is required")
	}
	if name == "" {
		name = "registry"
	}
	return &CatalogReader{
		catalog: newToolsetCatalog(newRedisCatalogStore(client, name), newRedisTimeSource(client)),
	}, nil
}

// ListToolsets returns active saved toolset summaries matching all requested
// tags. It reads no declarations and does not remove expired provider leases.
func (r *CatalogReader) ListToolsets(ctx context.Context, p *genregistry.ListToolsetsPayload) (*genregistry.ListToolsetsResult, error) {
	return r.catalog.listToolsets(ctx, p)
}

// ReadCatalogToolset returns one active registration and its saved resource
// owner within a positive raw state-and-definition byte budget. ServiceHealth
// is absent because a reader has no running Registry's health configuration.
// Missing, retired, unowned, malformed, and over-budget records retain the
// errors returned by Service.ReadCatalogToolset.
func (r *CatalogReader) ReadCatalogToolset(ctx context.Context, name string, rawByteBudget int64) (*CatalogToolset, error) {
	read, err := r.catalog.readCatalogToolset(ctx, name, rawByteBudget)
	if err != nil {
		return nil, err
	}
	return read.toolset, nil
}

// listToolsets constructs the generated listing result for both local readers
// and the running Service, preserving the same tag filtering and error context.
func (c *toolsetCatalog) listToolsets(ctx context.Context, p *genregistry.ListToolsetsPayload) (*genregistry.ListToolsetsResult, error) {
	toolsets, err := c.ListToolsets(ctx, p.Tags)
	if err != nil {
		return nil, fmt.Errorf("list toolsets: %w", err)
	}
	return &genregistry.ListToolsetsResult{Toolsets: toolsets}, nil
}
