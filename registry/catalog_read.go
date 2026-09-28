// Package registry reads one explicitly owned catalog record without scanning
// all declarations or pruning provider leases. Raw-byte limits apply only to
// this read and never change which declarations may be registered.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	// CatalogToolset pairs immutable application ownership with the exact
	// declaration and registration selected at one Redis instant.
	CatalogToolset struct {
		// Identity is the explicitly saved application resource identity.
		Identity CatalogIdentity
		// Registration contains the complete declaration and exact saved token.
		Registration *genregistry.ResolvedToolset
		// NativeAgent means the application must observe its configured worker.
		NativeAgent bool
		// ServiceHealth is populated only for service declarations. A native
		// declaration deliberately has no service-provider health value.
		ServiceHealth *ToolsetHealth
	}

	catalogSnapshot struct {
		state, definition string
		retired, exists   bool
		now               time.Time
	}
)

var (
	// ErrCatalogReadBudget means reading this record would exceed the caller's
	// raw state+definition byte budget. The record remains registered.
	ErrCatalogReadBudget = errors.New("catalog read exceeds raw-byte budget")
	// ErrCatalogIdentityMissing means historical ownership must be assigned
	// explicitly before this record can be read through an application catalog.
	ErrCatalogIdentityMissing = errors.New("catalog record has no application identity")

	catalogBoundedSnapshotScript = redis.NewScript(`
local has_state = redis.call("HEXISTS", KEYS[1], ARGV[1])
local has_definition = redis.call("HEXISTS", KEYS[2], ARGV[1])
if has_state ~= has_definition then
  return redis.error_reply("CATALOGINCOMPLETE")
end
if has_state == 0 then
  return {"", "", "0", "0", "0"}
end
local state_bytes = redis.call("HSTRLEN", KEYS[1], ARGV[1])
local definition_bytes = redis.call("HSTRLEN", KEYS[2], ARGV[1])
if state_bytes + definition_bytes > tonumber(ARGV[2]) then
  return redis.error_reply("CATALOGREADBUDGET")
end
local state = redis.call("HGET", KEYS[1], ARGV[1])
local definition = redis.call("HGET", KEYS[2], ARGV[1])
local entry = cjson.decode(state)
if type(entry.registration_token) ~= "string" then
  return redis.error_reply("CATALOGINVALIDTOKEN")
end
local retired = redis.call("SISMEMBER", KEYS[3], entry.registration_token)
local now = redis.call("TIME")
return {state, definition, tostring(retired), now[1], now[2]}
`)
)

// ReadCatalogToolset reads one record within a positive raw-byte budget and
// returns its exact service health observation. It never supplies Agent-worker
// availability or infers ownership for historical records.
func (s *Service) ReadCatalogToolset(ctx context.Context, name string, rawByteBudget int64) (*CatalogToolset, error) {
	if rawByteBudget <= 0 {
		return nil, errors.New("catalog read requires a positive raw-byte budget")
	}
	snapshot, err := s.catalog.store.BoundedSnapshot(ctx, toolsetCatalogKey(name), rawByteBudget)
	if err != nil {
		return nil, err
	}
	if !snapshot.exists {
		return nil, genregistry.MakeNotFound(errToolsetNotFound)
	}
	entry, err := s.catalog.decodeSnapshot(name, snapshot.state, snapshot.definition, snapshot.retired)
	if err != nil {
		return nil, err
	}
	if entry.State != catalogEntryActive {
		return nil, genregistry.MakeNotFound(errToolsetNotFound)
	}
	if entry.Identity == nil {
		return nil, ErrCatalogIdentityMissing
	}
	toolset, err := entry.Toolset.decode(entry.RegisteredAt)
	if err != nil {
		return nil, err
	}
	result := &CatalogToolset{
		Identity: *entry.Identity, NativeAgent: entry.NativeAgent,
		Registration: &genregistry.ResolvedToolset{
			Toolset: toolset, RegistrationToken: entry.RegistrationToken,
		},
	}
	if !entry.NativeAgent {
		health := s.catalogHealth(entry.catalogState, snapshot.now)
		result.ServiceHealth = &health
	}
	return result, nil
}

// CatalogRoutesAfter returns at most count currently indexed routes after the
// exclusive lexical position. It reads no declarations; the application owns
// page size, scope authorization, and its complete response budget.
func (s *Service) CatalogRoutesAfter(ctx context.Context, scope, after string, count int) ([]string, error) {
	if scope == "" || count <= 0 {
		return nil, errors.New("catalog range requires a scope and positive count")
	}
	return s.catalog.store.After(ctx, scope, after, count)
}

// CatalogContains reports whether one exact route is currently indexed in a
// scope. It reads no definition and grants no authority to use that route.
func (s *Service) CatalogContains(ctx context.Context, scope, name string) (bool, error) {
	if scope == "" || name == "" {
		return false, errors.New("catalog membership requires a scope and route")
	}
	return s.catalog.store.Contains(ctx, scope, name)
}

func (s *redisCatalogStore) BoundedSnapshot(ctx context.Context, key string, budget int64) (catalogSnapshot, error) {
	values, err := catalogBoundedSnapshotScript.Run(ctx, s.redis, []string{s.state, s.definitions, s.retired}, key, budget).StringSlice()
	if err != nil {
		if redis.HasErrorPrefix(err, "CATALOGREADBUDGET") {
			return catalogSnapshot{}, ErrCatalogReadBudget
		}
		return catalogSnapshot{}, fmt.Errorf("read bounded catalog record: %w", err)
	}
	if len(values) != 5 {
		return catalogSnapshot{}, fmt.Errorf("bounded catalog read returned %d values", len(values))
	}
	seconds, err := strconv.ParseInt(values[3], 10, 64)
	if err != nil {
		return catalogSnapshot{}, fmt.Errorf("decode catalog clock seconds: %w", err)
	}
	micros, err := strconv.ParseInt(values[4], 10, 64)
	if err != nil {
		return catalogSnapshot{}, fmt.Errorf("decode catalog clock microseconds: %w", err)
	}
	return catalogSnapshot{
		state: values[0], definition: values[1], retired: values[2] == "1",
		exists: values[0] != "", now: time.Unix(seconds, micros*1000),
	}, nil
}

func (s *redisCatalogStore) After(ctx context.Context, scope, after string, count int) ([]string, error) {
	minimum := "-"
	if after != "" {
		minimum = "(" + after
	}
	return s.redis.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key: s.scopeIndex(scope), Start: minimum, Stop: "+", ByLex: true, Offset: 0, Count: int64(count),
	}).Result()
}

func (s *redisCatalogStore) Contains(ctx context.Context, scope, name string) (bool, error) {
	_, err := s.redis.ZScore(ctx, s.scopeIndex(scope), name).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return err == nil, err
}
