// Package registry reads one explicitly owned catalog record without scanning
// all declarations or pruning provider leases. Active reads require a serving
// registration; saved reads preserve declarations after retirement. Both apply
// the same byte budget and saved-data checks without granting execution rights.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
		// ServiceHealth is populated only by Service.ReadCatalogToolset for active
		// service declarations. Saved reads and CatalogReader leave it absent.
		ServiceHealth *ToolsetHealth
	}

	// catalogToolsetRead keeps the selected state and clock with its decoded
	// result so Service can add health using its configured ping settings.
	catalogToolsetRead struct {
		toolset *CatalogToolset
		state   catalogState
		now     time.Time
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
  return {"", "", "0", "0", "0", ""}
end
local state_bytes = redis.call("HSTRLEN", KEYS[1], ARGV[1])
local definition = redis.call("HGET", KEYS[2], ARGV[1])
local definition_bytes = #definition
local blob = ""
if string.sub(definition, 1, 29) == '{"blob":"registry:definition:' then
  local ptr = cjson.decode(definition)
  if type(ptr.blob) ~= "string" or type(ptr.len) ~= "number" then
    return redis.error_reply("CATALOGINCOMPLETE")
  end
  definition_bytes = ptr.len
  if state_bytes + definition_bytes > tonumber(ARGV[2]) then
    return redis.error_reply("CATALOGREADBUDGET")
  end
  blob = redis.call("HGET", KEYS[2], ptr.blob)
  if not blob then
    return redis.error_reply("CATALOGINCOMPLETE")
  end
elseif state_bytes + definition_bytes > tonumber(ARGV[2]) then
  return redis.error_reply("CATALOGREADBUDGET")
end
local state = redis.call("HGET", KEYS[1], ARGV[1])
local entry = cjson.decode(state)
if type(entry.registration_token) ~= "string" then
  return redis.error_reply("CATALOGINVALIDTOKEN")
end
local retired = redis.call("SISMEMBER", KEYS[3], entry.registration_token)
local now = redis.call("TIME")
return {state, definition, tostring(retired), now[1], now[2], blob}
`)
)

// ReadCatalogToolset reads one record within a positive raw-byte budget and
// returns its exact service health observation. It never supplies Agent-worker
// availability or infers ownership for historical records.
func (s *Service) ReadCatalogToolset(ctx context.Context, name string, rawByteBudget int64) (*CatalogToolset, error) {
	read, err := s.catalog.readCatalogToolset(ctx, name, rawByteBudget)
	if err != nil {
		return nil, err
	}
	if !read.toolset.NativeAgent {
		health := s.catalogHealth(read.state, read.now)
		read.toolset.ServiceHealth = &health
	}
	return read.toolset, nil
}

// ReadSavedCatalogToolset receives an exact route and a positive byte budget,
// validates its saved declaration and owner, and returns an independent copy even
// after retirement. It reports no provider health and grants no permission to
// attach a provider or execute a new call. The host must authorize the read.
func (s *Service) ReadSavedCatalogToolset(ctx context.Context, name string, rawByteBudget int64) (_ *CatalogToolset, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.saved_read",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	defer finishCatalogSpan(ctx, span, &err)
	entry, now, err := s.catalog.readCatalogEntry(ctx, name, rawByteBudget)
	if err != nil {
		return nil, err
	}
	read, err := copyCatalogToolset(ctx, name, entry, now)
	if err != nil {
		return nil, err
	}
	return read.toolset, nil
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

// readCatalogToolset receives one exact route, validates its saved record, and
// rejects retired registrations before returning a declaration for active use.
func (c *toolsetCatalog) readCatalogToolset(ctx context.Context, name string, rawByteBudget int64) (_ *catalogToolsetRead, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.read",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	defer finishCatalogSpan(ctx, span, &err)
	entry, now, err := c.readCatalogEntry(ctx, name, rawByteBudget)
	if err != nil {
		return nil, err
	}
	if entry.State != catalogEntryActive {
		c.forgetDefinition(name)
		return nil, genregistry.MakeNotFound(errToolsetNotFound)
	}
	return copyCatalogToolset(ctx, name, entry, now)
}

// readCatalogEntry loads one coherent saved record and validates its declaration.
// Each caller checks the state it permits before requiring application identity.
func (c *toolsetCatalog) readCatalogEntry(ctx context.Context, name string, rawByteBudget int64) (_ catalogEntry, _ time.Time, err error) {
	if rawByteBudget <= 0 {
		return catalogEntry{}, time.Time{}, errors.New("catalog read requires a positive raw-byte budget")
	}
	// Read fresh state, definition bytes, retirement history, and the clock
	// together before using any retained validation.
	snapshotCtx, snapshotSpan := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.snapshot.read",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	snapshot, err := c.store.BoundedSnapshot(snapshotCtx, toolsetCatalogKey(name), rawByteBudget)
	if err == nil {
		snapshotSpan.SetAttributes(
			attribute.Int("toolregistry.catalog.state_read_bytes", len(snapshot.state)),
			attribute.Int("toolregistry.catalog.definition_read_bytes", len(snapshot.definition)),
		)
	}
	finishCatalogSpan(snapshotCtx, snapshotSpan, &err)
	if err != nil {
		return catalogEntry{}, time.Time{}, err
	}
	if !snapshot.exists {
		c.forgetDefinition(name)
		return catalogEntry{}, time.Time{}, genregistry.MakeNotFound(errToolsetNotFound)
	}
	entry, err := c.decodeSnapshot(ctx, name, snapshot.state, snapshot.definition, snapshot.retired)
	if err != nil {
		return catalogEntry{}, time.Time{}, err
	}
	return entry, snapshot.now, nil
}

// copyCatalogToolset requires saved application ownership and decodes the exact
// declaration so a caller cannot mutate data retained by another reader.
func copyCatalogToolset(ctx context.Context, name string, entry catalogEntry, now time.Time) (_ *catalogToolsetRead, err error) {
	if entry.Identity == nil {
		return nil, ErrCatalogIdentityMissing
	}
	// Decode the checked bytes into a separate object so the caller can change
	// its returned toolset without changing another read.
	decodeCtx, decodeSpan := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.definition.decode",
		trace.WithAttributes(attribute.String("toolregistry.toolset", name)))
	toolset, err := entry.Toolset.decode(entry.RegisteredAt)
	finishCatalogSpan(decodeCtx, decodeSpan, &err)
	if err != nil {
		return nil, err
	}
	result := &CatalogToolset{
		Identity: *entry.Identity, NativeAgent: entry.NativeAgent,
		Registration: &genregistry.ResolvedToolset{
			Toolset: toolset, RegistrationToken: entry.RegistrationToken,
		},
	}
	return &catalogToolsetRead{toolset: result, state: entry.catalogState, now: now}, nil
}

func (s *redisCatalogStore) BoundedSnapshot(ctx context.Context, key string, budget int64) (catalogSnapshot, error) {
	values, err := catalogBoundedSnapshotScript.Run(ctx, s.redis, []string{s.state, s.definitions, s.retired}, key, budget).StringSlice()
	if err != nil {
		if redis.HasErrorPrefix(err, "CATALOGREADBUDGET") {
			return catalogSnapshot{}, ErrCatalogReadBudget
		}
		return catalogSnapshot{}, fmt.Errorf("read bounded catalog record: %w", err)
	}
	if len(values) != 6 {
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
	definition := values[1]
	if values[0] != "" {
		definition, err = expandCatalogDefinition(values[1], values[5])
		if err != nil {
			return catalogSnapshot{}, fmt.Errorf("read bounded catalog record: %w", err)
		}
	}
	return catalogSnapshot{
		state: values[0], definition: definition, retired: values[2] == "1",
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
