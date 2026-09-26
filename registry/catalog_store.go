// Package registry keeps definitions separate from frequently updated provider
// state. The catalog owns both hashes and permanent retired tokens. Its writes
// compare the previous state and update all affected records in one Redis script.
package registry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/redis/go-redis/v9"
)

type (
	// catalogStore is the catalog's atomic persistence contract. Definitions
	// are read together with state, and retirement commits with replacement.
	catalogStore interface {
		Read(context.Context, string) (string, bool, error)
		Snapshot(context.Context, string) (string, string, bool, bool, error)
		BoundedSnapshot(context.Context, string, int64) (catalogSnapshot, error)
		After(context.Context, string, string, int) ([]string, error)
		Contains(context.Context, string, string) (bool, error)
		Keys(context.Context) ([]string, error)
		DefinitionKeys(context.Context) ([]string, error)
		Retired(context.Context, string) (bool, error)
		RetiredTokens(context.Context) ([]string, error)
		Commit(context.Context, string, string, catalogWrite) (bool, error)
	}

	// catalogWrite describes one catalog-owned transition. Definition bytes
	// are supplied only when registration changes them. LiveLease requires
	// the previous lease to remain valid when Redis commits the update.
	// A positive RoutableUntilUnixMilli requires existing routable membership
	// to survive until commit; expiration retries without writing stale health.
	catalogWrite struct {
		State                  string
		Definition             string
		CandidateToken         string
		RetireToken            string
		LiveLease              string
		RoutableUntilUnixMilli int64
		Scope                  string
		Indexed                bool
	}

	redisCatalogStore struct {
		redis       *redis.Client
		state       string
		definitions string
		retired     string
		indexPrefix string
	}
)

var (
	catalogReadScript = redis.NewScript(`
local state = redis.call("HGET", KEYS[1], ARGV[1])
if state and redis.call("HEXISTS", KEYS[2], ARGV[1]) == 0 then
  return redis.error_reply("CATALOGINCOMPLETE")
end
return state
`)

	catalogSnapshotScript = redis.NewScript(`
local state = redis.call("HGET", KEYS[1], ARGV[1])
local definition = redis.call("HGET", KEYS[2], ARGV[1])
if (state and not definition) or (definition and not state) then
  return redis.error_reply("CATALOGINCOMPLETE")
end
if not state then
  return {"", "", "0"}
end
local entry = cjson.decode(state)
if type(entry.registration_token) ~= "string" then
  return redis.error_reply("CATALOGINVALIDTOKEN")
end
local retired = redis.call("SISMEMBER", KEYS[3], entry.registration_token)
return {state, definition, tostring(retired)}
`)

	catalogCommitScript = redis.NewScript(`
local current = redis.call("HGET", KEYS[1], ARGV[1])
if (current or "") ~= ARGV[2] then
  return 0
end
local definition_exists = redis.call("HEXISTS", KEYS[2], ARGV[1]) == 1
if (current and not definition_exists) or (not current and definition_exists) then
  return redis.error_reply("CATALOGINCOMPLETE")
end
if ARGV[5] ~= "" and redis.call("SISMEMBER", KEYS[3], ARGV[5]) == 1 then
  return redis.error_reply("CATALOGRETIRED")
end
if ARGV[7] ~= "" then
  if not current then
    return redis.error_reply("PROVIDERLEASELOST")
  end
  local entry = cjson.decode(current)
  local lease = entry.provider_leases[ARGV[7]]
  local now = redis.call("TIME")
  local now_millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
  if not lease or tonumber(lease.expires_at_unix_milli) <= now_millis then
    return redis.error_reply("PROVIDERLEASELOST")
  end
end
if tonumber(ARGV[8]) > 0 then
  local now = redis.call("TIME")
  local now_millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
  if now_millis >= tonumber(ARGV[8]) then
    return 0
  end
end
if ARGV[9] ~= "" then
  local index_type = redis.call("TYPE", KEYS[4]).ok
  if index_type ~= "none" and index_type ~= "zset" then
    return redis.error_reply("CATALOGINVALIDINDEX")
  end
end
if ARGV[4] ~= "" then
  redis.call("HSET", KEYS[2], ARGV[1], ARGV[4])
end
if ARGV[6] ~= "" then
  redis.call("SADD", KEYS[3], ARGV[6])
end
redis.call("HSET", KEYS[1], ARGV[1], ARGV[3])
if ARGV[9] == "active" then
  redis.call("ZADD", KEYS[4], 0, ARGV[10])
elseif ARGV[9] == "retired" then
  redis.call("ZREM", KEYS[4], ARGV[10])
end
return 1
`)
)

// newRedisCatalogStore retains the state hash address used by saved call
// operations. The contents are compact state; no replicated map is joined.
func newRedisCatalogStore(client *redis.Client, name string) *redisCatalogStore {
	return &redisCatalogStore{
		redis:       client,
		state:       catalogStateHashKey(name),
		definitions: "registry:" + name + ":definitions",
		retired:     "registry:" + name + ":retired",
		indexPrefix: "registry:" + name + ":scope:",
	}
}

func catalogStateHashKey(name string) string {
	return "map:" + name + ":toolsets:content"
}

func (s *redisCatalogStore) Read(ctx context.Context, key string) (raw string, exists bool, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.state.read",
		trace.WithAttributes(attribute.String("toolregistry.toolset", strings.TrimPrefix(key, toolsetCatalogKeyPrefix))))
	defer finishCatalogSpan(ctx, span, &err)
	value, err := catalogReadScript.Run(ctx, s.redis, []string{s.state, s.definitions}, key).Text()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read catalog state: %w", err)
	}
	span.SetAttributes(attribute.Int("toolregistry.catalog.state_read_bytes", len(value)))
	return value, true, nil
}

func (s *redisCatalogStore) Snapshot(ctx context.Context, key string) (string, string, bool, bool, error) {
	values, err := catalogSnapshotScript.Run(ctx, s.redis, []string{s.state, s.definitions, s.retired}, key).StringSlice()
	if err != nil {
		return "", "", false, false, fmt.Errorf("read catalog definition and state: %w", err)
	}
	if len(values) != 3 {
		return "", "", false, false, fmt.Errorf("catalog snapshot returned %d values", len(values))
	}
	return values[0], values[1], values[2] == "1", values[0] != "", nil
}

func (s *redisCatalogStore) Keys(ctx context.Context) ([]string, error) {
	return s.redis.HKeys(ctx, s.state).Result()
}

func (s *redisCatalogStore) DefinitionKeys(ctx context.Context) ([]string, error) {
	return s.redis.HKeys(ctx, s.definitions).Result()
}

func (s *redisCatalogStore) Retired(ctx context.Context, token string) (bool, error) {
	return s.redis.SIsMember(ctx, s.retired, token).Result()
}

func (s *redisCatalogStore) RetiredTokens(ctx context.Context) ([]string, error) {
	return s.redis.SMembers(ctx, s.retired).Result()
}

func (s *redisCatalogStore) Commit(ctx context.Context, key, previous string, next catalogWrite) (committed bool, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.state.update",
		trace.WithAttributes(
			attribute.String("toolregistry.toolset", strings.TrimPrefix(key, toolsetCatalogKeyPrefix)),
			attribute.Int("toolregistry.catalog.state_compare_bytes", len(previous)),
			attribute.Int("toolregistry.catalog.state_write_bytes", len(next.State)),
			attribute.Int("toolregistry.catalog.definition_write_bytes", len(next.Definition)),
		))
	defer finishCatalogSpan(ctx, span, &err)
	indexState := ""
	keys := []string{s.state, s.definitions, s.retired}
	if next.Scope != "" {
		keys = append(keys, s.scopeIndex(next.Scope))
		indexState = string(catalogEntryRetired)
		if next.Indexed {
			indexState = string(catalogEntryActive)
		}
	}
	result, err := catalogCommitScript.Run(ctx, s.redis,
		keys,
		key, previous, next.State, next.Definition, next.CandidateToken, next.RetireToken, next.LiveLease, next.RoutableUntilUnixMilli,
		indexState, strings.TrimPrefix(key, toolsetCatalogKeyPrefix),
	).Int()
	if err != nil {
		switch {
		case redis.HasErrorPrefix(err, "CATALOGRETIRED"):
			return false, errAdmissionRetired
		case redis.HasErrorPrefix(err, "PROVIDERLEASELOST"):
			return false, fmt.Errorf("%w: lease expired before commit", errProviderLeaseLost)
		default:
			return false, fmt.Errorf("commit catalog state: %w", err)
		}
	}
	span.SetAttributes(attribute.Bool("toolregistry.catalog.conditional_retry", result == 0))
	return result == 1, nil
}

func (s *redisCatalogStore) scopeIndex(scope string) string {
	return fmt.Sprintf("%s%x", s.indexPrefix, sha256.Sum256([]byte(scope)))
}

// finishCatalogSpan reports storage failures without turning caller cancellation
// into an infrastructure fault. It never records schemas, arguments, or results.
func finishCatalogSpan(ctx context.Context, span trace.Span, err *error) {
	if *err != nil && (ctx.Err() == nil || !catalogCancellationOnly(*err)) {
		span.RecordError(*err)
		span.SetStatus(codes.Error, "catalog operation failed")
	} else if *err == nil {
		span.SetStatus(codes.Ok, "completed")
	}
	span.End()
}

// catalogCancellationOnly checks every actual cause so a storage failure joined
// with cancellation still appears in the operation's trace.
func catalogCancellationOnly(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !catalogCancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return catalogCancellationOnly(cause)
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
