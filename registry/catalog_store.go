// Package registry keeps definitions separate from frequently updated provider
// state. The catalog owns both hashes and permanent retired tokens. Its writes
// compare the previous state and update all affected records in one Redis script.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/redis/go-redis/v9"
	genregistry "goa.design/goa-ai/registry/gen/registry"
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
		ResourceKind           string
		ResourceName           string
	}

	redisCatalogStore struct {
		redis       *redis.Client
		state       string
		definitions string
		retired     string
		indexPrefix string
	}

	// catalogDefinitionPointer binds one route key in the definitions hash to a
	// content-addressed canonical schema body in the same hash.
	catalogDefinitionPointer struct {
		Blob          string `json:"blob"`
		Len           int    `json:"len"`
		Name          string `json:"name"`
		Configuration string `json:"configuration,omitempty"`
	}
)

const (
	catalogDefinitionBlobPrefix    = "registry:definition:"
	catalogDefinitionPointerPrefix = `{"blob":"registry:definition:`
)

var (
	catalogReadScript = redis.NewScript(`
local state = redis.call("HGET", KEYS[1], ARGV[1])
local definition = redis.call("HGET", KEYS[2], ARGV[1])
if state and not definition then
  return redis.error_reply("CATALOGINCOMPLETE")
end
if state and string.sub(definition, 1, 29) == '{"blob":"registry:definition:' then
  local ptr = cjson.decode(definition)
  if type(ptr.blob) ~= "string" or redis.call("HEXISTS", KEYS[2], ptr.blob) == 0 then
    return redis.error_reply("CATALOGINCOMPLETE")
  end
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
  return {"", "", "0", ""}
end
local blob = ""
if string.sub(definition, 1, 29) == '{"blob":"registry:definition:' then
  local ptr = cjson.decode(definition)
  if type(ptr.blob) ~= "string" then
    return redis.error_reply("CATALOGINCOMPLETE")
  end
  blob = redis.call("HGET", KEYS[2], ptr.blob)
  if not blob then
    return redis.error_reply("CATALOGINCOMPLETE")
  end
end
local entry = cjson.decode(state)
if type(entry.registration_token) ~= "string" then
  return redis.error_reply("CATALOGINVALIDTOKEN")
end
local retired = redis.call("SISMEMBER", KEYS[3], entry.registration_token)
return {state, definition, tostring(retired), blob}
`)

	// catalogCommitScript atomically compares previous state and updates state,
	// deduplicated definition pointer/body, retired tokens, and scope/resource
	// indices in one Redis transaction:
	// KEYS: [1]=state, [2]=definitions, [3]=retired, [4]=scopeIndex,
	//       [5]=resourceIndex, [6]=ownerResourcesIndex
	// ARGV: [1]=key, [2]=previous, [3]=state, [4]=routeDefinition,
	//       [5]=candidateToken, [6]=retireToken, [7]=liveLease,
	//       [8]=routableUntilUnixMilli, [9]=indexState, [10]=route,
	//       [11]=encodedResource, [12]=blobKey, [13]=blobRaw
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
  for index = 4, 6 do
    local index_type = redis.call("TYPE", KEYS[index]).ok
    if index_type ~= "none" and index_type ~= "zset" then
      return redis.error_reply("CATALOGINVALIDINDEX")
    end
  end
end
if ARGV[4] ~= "" then
  if ARGV[12] ~= "" then
    redis.call("HSET", KEYS[2], ARGV[12], ARGV[13])
  end
  redis.call("HSET", KEYS[2], ARGV[1], ARGV[4])
end
if ARGV[6] ~= "" then
  redis.call("SADD", KEYS[3], ARGV[6])
end
redis.call("HSET", KEYS[1], ARGV[1], ARGV[3])
if ARGV[9] == "active" then
  redis.call("ZADD", KEYS[4], 0, ARGV[10])
  redis.call("ZADD", KEYS[5], 0, ARGV[10])
  redis.call("ZADD", KEYS[6], 0, ARGV[11])
elseif ARGV[9] == "retired" then
  redis.call("ZREM", KEYS[4], ARGV[10])
  redis.call("ZREM", KEYS[5], ARGV[10])
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
	if len(values) != 4 {
		return "", "", false, false, fmt.Errorf("catalog snapshot returned %d values", len(values))
	}
	if values[0] == "" {
		return "", "", false, false, nil
	}
	definition, err := expandCatalogDefinition(values[1], values[3])
	if err != nil {
		return "", "", false, false, err
	}
	return values[0], definition, values[2] == "1", true, nil
}

func (s *redisCatalogStore) Keys(ctx context.Context) ([]string, error) {
	return s.redis.HKeys(ctx, s.state).Result()
}

func (s *redisCatalogStore) DefinitionKeys(ctx context.Context) ([]string, error) {
	keys, err := s.redis.HKeys(ctx, s.definitions).Result()
	if err != nil {
		return nil, err
	}
	filtered := keys[:0]
	for _, key := range keys {
		if !strings.HasPrefix(key, catalogDefinitionBlobPrefix) {
			filtered = append(filtered, key)
		}
	}
	return filtered, nil
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
	resourceMember := ""
	keys := []string{s.state, s.definitions, s.retired}
	if next.Scope != "" {
		resource := &genregistry.CatalogResource{Kind: next.ResourceKind, Scope: next.Scope, Name: next.ResourceName}
		encoded, encodeErr := genregistry.EncodeCatalogResource(resource)
		if encodeErr != nil {
			return false, encodeErr
		}
		resourceMember = string(encoded)
		keys = append(keys, s.scopeIndex(next.Scope), s.resourceIndex(resource), s.ownerResourcesIndex(next.Scope, next.ResourceKind))
		indexState = string(catalogEntryRetired)
		if next.Indexed {
			indexState = string(catalogEntryActive)
		}
	}
	routeDefinition, blobKey, blobRaw := "", "", ""
	if next.Definition != "" {
		routeDefinition, blobKey, blobRaw = encodeCatalogDefinitionRecord(next.Definition)
	}
	result, err := catalogCommitScript.Run(ctx, s.redis,
		keys,
		key, previous, next.State, routeDefinition, next.CandidateToken, next.RetireToken, next.LiveLease, next.RoutableUntilUnixMilli,
		indexState, strings.TrimPrefix(key, toolsetCatalogKeyPrefix), resourceMember, blobKey, blobRaw,
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

// encodeCatalogDefinitionRecord separates a canonical toolset declaration into
// a content-addressed schema body and a compact route-specific pointer when
// exact byte reconstruction is possible. Non-canonical payloads stay inline.
func encodeCatalogDefinitionRecord(raw string) (string, string, string) {
	if !strings.HasPrefix(raw, `{"Name":`) {
		return raw, "", ""
	}
	var toolset genregistry.Toolset
	if err := json.Unmarshal([]byte(raw), &toolset); err != nil || toolset.Name == "" {
		return raw, "", ""
	}
	nameJSON, err := json.Marshal(toolset.Name)
	if err != nil {
		return raw, "", ""
	}
	prefix := `{"Name":` + string(nameJSON) + `,`
	if !strings.HasPrefix(raw, prefix) {
		return raw, "", ""
	}
	blob := `{"Name":"",` + raw[len(prefix):]
	configuration := ""
	if len(toolset.Tools) > 0 && toolset.Tools[0] != nil &&
		toolset.Tools[0].ConsumerContract != nil && toolset.Tools[0].ConsumerContract.Agent != nil {
		candidate := toolset.Tools[0].ConsumerContract.Agent.Configuration
		allSame := candidate != ""
		for _, tool := range toolset.Tools[1:] {
			if tool == nil || tool.ConsumerContract == nil || tool.ConsumerContract.Agent == nil ||
				tool.ConsumerContract.Agent.Configuration != candidate {
				allSame = false
				break
			}
		}
		if allSame {
			configJSON, marshalErr := json.Marshal(candidate)
			if marshalErr == nil {
				configuration = candidate
				blob = strings.ReplaceAll(
					blob,
					`"Configuration":`+string(configJSON)+`,"Workflow":`,
					`"Configuration":"","Workflow":`,
				)
			}
		}
	}
	blobKey := fmt.Sprintf("%s%x", catalogDefinitionBlobPrefix, sha256.Sum256([]byte(blob)))
	pointerBytes, err := json.Marshal(catalogDefinitionPointer{
		Blob:          blobKey,
		Len:           len(raw),
		Name:          toolset.Name,
		Configuration: configuration,
	})
	if err != nil {
		return raw, "", ""
	}
	pointer := string(pointerBytes)
	reconstructed, err := expandCatalogDefinition(pointer, blob)
	if err != nil || reconstructed != raw {
		return raw, "", ""
	}
	return pointer, blobKey, blob
}

// expandCatalogDefinition restores the exact serialized toolset declaration
// from a deduplicated pointer and shared schema body without re-parsing the
// toolset schema tree. Inline declarations are returned unchanged.
func expandCatalogDefinition(stored, blob string) (string, error) {
	if !strings.HasPrefix(stored, catalogDefinitionPointerPrefix) {
		return stored, nil
	}
	var ptr catalogDefinitionPointer
	if err := json.Unmarshal([]byte(stored), &ptr); err != nil {
		return "", fmt.Errorf("decode catalog definition pointer: %w", err)
	}
	if !strings.HasPrefix(blob, `{"Name":"",`) {
		return "", errors.New("catalog definition blob has invalid prefix")
	}
	nameJSON, err := json.Marshal(ptr.Name)
	if err != nil {
		return "", fmt.Errorf("encode catalog definition name: %w", err)
	}
	expanded := `{"Name":` + string(nameJSON) + `,` + blob[len(`{"Name":"",`):]
	if ptr.Configuration != "" {
		configJSON, err := json.Marshal(ptr.Configuration)
		if err != nil {
			return "", fmt.Errorf("encode catalog definition configuration: %w", err)
		}
		expanded = strings.ReplaceAll(
			expanded,
			`"Configuration":"","Workflow":`,
			`"Configuration":`+string(configJSON)+`,"Workflow":`,
		)
	}
	if len(expanded) != ptr.Len {
		return "", errors.New("catalog definition pointer length does not match expanded definition")
	}
	return expanded, nil
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
