// Package registry owns the toolset catalog used by the gateway. The catalog is
// persisted directly in the registry replicated-map keyspace so all registry
// nodes share one canonical view of registration state.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	// catalogMap captures the replicated-map operations the registry catalog needs.
	// The concrete production implementation is `*rmap.Map`; tests use small in-memory fakes.
	catalogMap interface {
		Delete(ctx context.Context, key string) (string, error)
		Get(key string) (string, bool)
		Keys() []string
		Set(ctx context.Context, key, value string) (string, error)
	}

	// catalogEntry is the canonical persisted registry record.
	// The transport-facing toolset metadata stays separate from the internal
	// registration token so wall-clock timestamps are never treated as identity.
	catalogEntry struct {
		Toolset           *genregistry.Toolset `json:"toolset"`
		RegistrationToken string               `json:"registration_token"`
	}

	// toolsetCatalog persists toolsets in the registry replicated-map keyspace.
	// Entries are JSON encoded so every read returns a fresh value detached from
	// caller-owned memory and durable across process restarts.
	toolsetCatalog struct {
		m catalogMap
	}
)

const toolsetCatalogKeyPrefix = "registry:toolset:"

var errToolsetNotFound = errors.New("toolset not found")

// newToolsetCatalog constructs the canonical registry catalog over the provided
// replicated map. The caller owns the map lifecycle.
func newToolsetCatalog(m catalogMap) *toolsetCatalog {
	return &toolsetCatalog{m: m}
}

// SaveToolset stores or replaces a toolset entry under its deterministic catalog key.
func (c *toolsetCatalog) SaveToolset(ctx context.Context, toolset *genregistry.Toolset) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := catalogEntry{
		Toolset:           toolset,
		RegistrationToken: uuid.NewString(),
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal toolset %q: %w", toolset.Name, err)
	}
	if _, err := c.m.Set(ctx, toolsetCatalogKey(toolset.Name), string(body)); err != nil {
		return fmt.Errorf("store toolset %q: %w", toolset.Name, err)
	}
	return nil
}

// GetToolset loads and decodes a toolset by name. Missing entries return
// errToolsetNotFound so callers can map absence to the transport contract.
func (c *toolsetCatalog) GetToolset(ctx context.Context, name string) (*genregistry.Toolset, error) {
	entry, err := c.entry(ctx, name)
	if err != nil {
		return nil, err
	}
	return entry.Toolset, nil
}

// RegistrationToken loads the current registration epoch token for a toolset.
// The token changes on every save so same-name re-registration invalidates old
// health records and stale pongs.
func (c *toolsetCatalog) RegistrationToken(ctx context.Context, name string) (string, error) {
	entry, err := c.entry(ctx, name)
	if err != nil {
		return "", err
	}
	return entry.RegistrationToken, nil
}

// DeleteToolset removes a toolset entry. Deleting a missing toolset returns
// errToolsetNotFound so unregister can surface a precise not-found error.
func (c *toolsetCatalog) DeleteToolset(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := toolsetCatalogKey(name)
	if _, ok := c.m.Get(key); !ok {
		return errToolsetNotFound
	}
	if _, err := c.m.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete toolset %q: %w", name, err)
	}
	return nil
}

// ListToolsets returns every catalog entry whose tags satisfy the filter.
func (c *toolsetCatalog) ListToolsets(ctx context.Context, tags []string) ([]*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := c.m.Keys()
	toolsets := make([]*genregistry.Toolset, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
			continue
		}
		toolset, err := c.GetToolset(ctx, strings.TrimPrefix(key, toolsetCatalogKeyPrefix))
		if err != nil {
			return nil, err
		}
		if catalogMatchesTags(toolset.Tags, tags) {
			toolsets = append(toolsets, toolset)
		}
	}
	return toolsets, nil
}

// SearchToolsets returns catalog entries whose name, description, or tags match
// the query case-insensitively.
func (c *toolsetCatalog) SearchToolsets(ctx context.Context, query string) ([]*genregistry.Toolset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	keys := c.m.Keys()
	toolsets := make([]*genregistry.Toolset, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, toolsetCatalogKeyPrefix) {
			continue
		}
		toolset, err := c.GetToolset(ctx, strings.TrimPrefix(key, toolsetCatalogKeyPrefix))
		if err != nil {
			return nil, err
		}
		if catalogMatchesQuery(toolset, lowerQuery) {
			toolsets = append(toolsets, toolset)
		}
	}
	return toolsets, nil
}

// entry loads and validates the canonical persisted catalog record for a toolset.
func (c *toolsetCatalog) entry(ctx context.Context, name string) (catalogEntry, error) {
	if err := ctx.Err(); err != nil {
		return catalogEntry{}, err
	}
	body, ok := c.m.Get(toolsetCatalogKey(name))
	if !ok {
		return catalogEntry{}, errToolsetNotFound
	}
	return parseCatalogEntry(name, body)
}

// parseCatalogEntry decodes one catalog record and rejects incomplete payloads
// from the shared map so callers never mistake malformed state for a valid
// registration.
func parseCatalogEntry(name string, body string) (catalogEntry, error) {
	var entry catalogEntry
	if err := json.Unmarshal([]byte(body), &entry); err != nil {
		return catalogEntry{}, fmt.Errorf("unmarshal toolset %q: %w", name, err)
	}
	if entry.Toolset == nil {
		return catalogEntry{}, fmt.Errorf("toolset %q missing toolset payload", name)
	}
	if entry.RegistrationToken == "" {
		return catalogEntry{}, fmt.Errorf("toolset %q missing registration token", name)
	}
	return entry, nil
}

// toolsetCatalogKey returns the deterministic replicated-map key for a toolset.
func toolsetCatalogKey(name string) string {
	return toolsetCatalogKeyPrefix + name
}

// catalogMatchesTags reports whether the toolset tags contain every requested
// filter tag.
func catalogMatchesTags(toolsetTags, filterTags []string) bool {
	if len(filterTags) == 0 {
		return true
	}
	toolsetTagSet := make(map[string]struct{}, len(toolsetTags))
	for _, tag := range toolsetTags {
		toolsetTagSet[tag] = struct{}{}
	}
	for _, tag := range filterTags {
		if _, ok := toolsetTagSet[tag]; !ok {
			return false
		}
	}
	return true
}

// catalogMatchesQuery reports whether the search query matches the toolset name,
// description, or tags case-insensitively.
func catalogMatchesQuery(toolset *genregistry.Toolset, lowerQuery string) bool {
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
