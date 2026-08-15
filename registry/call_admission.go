// Package registry owns cross-replica admission for routed tool calls.
//
// One expiring Redis hash per transport identity stores the admitted or
// rejected decision. An admitted record binds retries to one immutable request,
// absolute result-history expiration, and terminal state. Its provider token
// may change before publication and becomes permanent when the request is
// appended. Request and terminal-result publication each commit into that
// record in the same Redis operation as their stream append.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// callAdmissionStore owns immutable call identity across registry nodes.
	callAdmissionStore struct {
		redis          *redis.Client
		prefix         string
		catalogHashKey string
		settlementKey  string
		membershipKey  string
	}

	// callAdmission is the persisted publication state for one immutable call.
	callAdmission struct {
		key                string
		digest             string
		executionDeadline  time.Time
		expiresAt          time.Time
		published          bool
		terminal           bool
		terminalEventID    string
		overloadEventID    string
		overloadRetryAfter time.Duration
		catalogHashKey     string
		catalogField       string
		registrationToken  string
	}

	// callClaimDisposition is the closed pre-dispatch state returned by Redis.
	callClaimDisposition string
)

const (
	callClaimExecute  callClaimDisposition = "execute"
	callClaimTerminal callClaimDisposition = "terminal"
	callClaimClaimed  callClaimDisposition = "claimed"
	callClaimExpired  callClaimDisposition = "expired"
)

var (
	errCallAdmissionConflict = errors.New("tool call admission conflicts with immutable request")
	errCallAdmissionNotFound = errors.New("tool call admission does not exist")
	errCallTerminalConflict  = errors.New("tool call terminal result conflicts with committed result")

	initializeResultStreamScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "digest") ~= ARGV[1] then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if redis.call("HGET", KEYS[1], "terminal") == "1"
or redis.call("XLEN", KEYS[2]) > 0 then
  return 0
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
if not expires or expires <= now_millis then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[4], "*", "n", ARGV[2], "p", ARGV[3])
redis.call("PEXPIREAT", KEYS[2], expires)
return 1
`)
	completeCallAdmissionScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "tool_use_id") ~= ARGV[1]
or redis.call("HGET", KEYS[1], "registration_token") ~= ARGV[2] then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
local raw = redis.call("HGET", KEYS[3], ARGV[3])
if not raw then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local entry = cjson.decode(raw)
if entry.registration_token ~= ARGV[4] then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local lease = entry.provider_leases[ARGV[5]]
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
if not lease or tonumber(lease.expires_at_unix_milli) <= now_millis then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
if redis.call("HEXISTS", KEYS[1], "claim:" .. ARGV[6]) == 0 then
  return redis.error_reply("CALLCLAIMCHANGED")
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
if not expires or expires <= now_millis then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if redis.call("HGET", KEYS[1], "dispatch_provider_token") ~= ARGV[4]
or redis.call("HGET", KEYS[1], "dispatch_provider_lease") ~= ARGV[5]
or redis.call("HGET", KEYS[1], "dispatch_request_event_id") ~= ARGV[6] then
  return redis.error_reply("DISPATCHCLAIMCHANGED")
end
if redis.call("HGET", KEYS[1], "terminal") == "1" then
  if redis.call("HGET", KEYS[1], "terminal_cause") == "execution_deadline" then
    return {2, redis.call("HGET", KEYS[1], "terminal_event_id") or ""}
  end
  if redis.call("HGET", KEYS[1], "terminal_digest") ~= ARGV[7] then
    return redis.error_reply("TERMINALCONFLICT")
  end
  return {0, redis.call("HGET", KEYS[1], "terminal_event_id") or ""}
end
local execution_deadline = tonumber(redis.call("HGET", KEYS[1], "execution_deadline_unix_milli"))
if not execution_deadline then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if execution_deadline <= now_millis then
  local payload = redis.call("HGET", KEYS[1], "outcome_unknown_payload")
  if not payload or payload == "" then
    return redis.error_reply("OUTCOMEUNKNOWNPAYLOADMISSING")
  end
  local id = redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[10], "*", "n", ARGV[8], "p", payload)
  redis.call("PEXPIREAT", KEYS[2], expires)
  redis.call(
    "HSET", KEYS[1],
    "terminal", "1",
    "terminal_event_id", id,
    "terminal_digest", redis.sha1hex(payload),
    "terminal_payload", payload,
    "terminal_cause", "execution_deadline"
  )
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("ZREM", KEYS[5], KEYS[1])
  redis.call("HDEL", KEYS[6], KEYS[1])
  return {2, id}
end
local id = redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[10], "*", "n", ARGV[8], "p", ARGV[9])
redis.call("PEXPIREAT", KEYS[2], expires)
redis.call(
  "HSET", KEYS[1],
  "terminal", "1",
  "terminal_event_id", id,
  "terminal_digest", ARGV[7],
  "terminal_payload", ARGV[9],
  "terminal_cause", "provider"
)
redis.call("ZREM", KEYS[4], KEYS[1])
redis.call("ZREM", KEYS[5], KEYS[1])
redis.call("HDEL", KEYS[6], KEYS[1])
return {1, id}
`)
	claimCallAdmissionScript = redis.NewScript(`
local raw = redis.call("HGET", KEYS[2], ARGV[3])
if not raw then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local entry = cjson.decode(raw)
if entry.registration_token ~= ARGV[4] then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local lease = entry.provider_leases[ARGV[5]]
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
if not lease or tonumber(lease.expires_at_unix_milli) <= now_millis then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
if redis.call("EXISTS", KEYS[1]) == 0 then
  return 4
end
if redis.call("HGET", KEYS[1], "tool_use_id") ~= ARGV[1]
or redis.call("HGET", KEYS[1], "registration_token") ~= ARGV[2] then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if redis.call("HEXISTS", KEYS[1], "claim:" .. ARGV[6]) == 0 then
  return redis.error_reply("CALLCLAIMCHANGED")
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
local execution_deadline = tonumber(redis.call("HGET", KEYS[1], "execution_deadline_unix_milli"))
if not expires or not execution_deadline then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if redis.call("HGET", KEYS[1], "terminal") == "1" then
  return 2
end
if execution_deadline <= now_millis then
  return 4
end
if redis.call("HGET", KEYS[1], "dispatch_provider_token") ~= "" then
  return 3
end
if ARGV[2] ~= ARGV[4] then
  local id = redis.call("XADD", KEYS[3], "MAXLEN", "=", ARGV[10], "*", "n", ARGV[7], "p", ARGV[8])
  redis.call("PEXPIREAT", KEYS[3], expires)
  redis.call(
    "HSET", KEYS[1],
    "terminal", "1",
    "terminal_event_id", id,
    "terminal_digest", ARGV[9],
    "terminal_payload", ARGV[8],
    "terminal_cause", "stale_admission"
  )
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("HDEL", KEYS[6], KEYS[1])
  return 2
end
redis.call(
  "HSET", KEYS[1],
  "dispatch_provider_token", ARGV[4],
  "dispatch_provider_lease", ARGV[5],
  "dispatch_request_event_id", ARGV[6],
  "dispatch_lease_expires_at_unix_milli", tostring(lease.expires_at_unix_milli)
)
local settlement_deadline = math.min(execution_deadline, tonumber(lease.expires_at_unix_milli))
redis.call("ZADD", KEYS[4], settlement_deadline, KEYS[1])
redis.call("ZADD", KEYS[5], execution_deadline, KEYS[1])
redis.call("HSET", KEYS[6], KEYS[1], KEYS[5])
return 1
`)
	publishLiveCallEventScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "tool_use_id") ~= ARGV[1]
or redis.call("HGET", KEYS[1], "registration_token") ~= ARGV[2] then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
local raw = redis.call("HGET", KEYS[3], ARGV[3])
if not raw then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local entry = cjson.decode(raw)
if entry.registration_token ~= ARGV[4] then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local lease = entry.provider_leases[ARGV[5]]
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
if not lease or tonumber(lease.expires_at_unix_milli) <= now_millis then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
if redis.call("HEXISTS", KEYS[1], "claim:" .. ARGV[6]) == 0 then
  return redis.error_reply("CALLCLAIMCHANGED")
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
if not expires or expires <= now_millis then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if redis.call("HGET", KEYS[1], "terminal") == "1" then
  return 0
end
local execution_deadline = tonumber(redis.call("HGET", KEYS[1], "execution_deadline_unix_milli"))
if not execution_deadline or execution_deadline <= now_millis then
  return 0
end
if redis.call("HGET", KEYS[1], "dispatch_provider_token") ~= ARGV[4]
or redis.call("HGET", KEYS[1], "dispatch_provider_lease") ~= ARGV[5]
or redis.call("HGET", KEYS[1], "dispatch_request_event_id") ~= ARGV[6] then
  return redis.error_reply("DISPATCHCLAIMCHANGED")
end
local count = tonumber(redis.call("HGET", KEYS[1], "output_delta_count") or "0")
if count >= tonumber(ARGV[9]) then
  return 0
end
redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[10], "*", "n", ARGV[7], "p", ARGV[8])
redis.call("HINCRBY", KEYS[1], "output_delta_count", 1)
redis.call("PEXPIREAT", KEYS[2], expires)
return 1
`)
	reportOverloadCallScript = redis.NewScript(`
local raw = redis.call("HGET", KEYS[3], ARGV[3])
if not raw then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local entry = cjson.decode(raw)
if entry.registration_token ~= ARGV[4] then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
local lease = entry.provider_leases[ARGV[5]]
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
if not lease or tonumber(lease.expires_at_unix_milli) <= now_millis then
  return redis.error_reply("PROVIDERLEASECHANGED")
end
if redis.call("EXISTS", KEYS[1]) == 0 then
  return 0
end
if redis.call("HGET", KEYS[1], "tool_use_id") ~= ARGV[1]
or redis.call("HGET", KEYS[1], "registration_token") ~= ARGV[2] then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if redis.call("HEXISTS", KEYS[1], "claim:" .. ARGV[6]) == 0 then
  return redis.error_reply("CALLCLAIMCHANGED")
end
local overload_request = "overload_request:" .. ARGV[6]
if redis.call("HEXISTS", KEYS[1], overload_request) == 1 then
  return 0
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
local execution_deadline = tonumber(redis.call("HGET", KEYS[1], "execution_deadline_unix_milli"))
if not expires or not execution_deadline then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
if expires <= now_millis or execution_deadline <= now_millis then
  return 0
end
if redis.call("HGET", KEYS[1], "terminal") == "1"
or redis.call("HGET", KEYS[1], "dispatch_provider_token") ~= "" then
  return 0
end
if ARGV[2] ~= ARGV[4] then
  local id = redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[12], "*", "n", ARGV[7], "p", ARGV[9])
  redis.call("PEXPIREAT", KEYS[2], expires)
  redis.call(
    "HSET", KEYS[1],
    "terminal", "1",
    "terminal_event_id", id,
    "terminal_digest", ARGV[10],
    "terminal_payload", ARGV[9],
    "terminal_cause", "stale_admission",
    overload_request, id
  )
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("HDEL", KEYS[5], KEYS[1])
  return 1
end
local id = redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[12], "*", "n", ARGV[7], "p", ARGV[8])
redis.call("PEXPIREAT", KEYS[2], expires)
redis.call(
  "HSET", KEYS[1],
  "overload_event_id", id,
  "overload_retry_after_ms", ARGV[11],
  overload_request, id
)
return 1
`)
	restoreCallTerminalScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "terminal") ~= "1" then
  return 0
end
local payload = redis.call("HGET", KEYS[1], "terminal_payload")
if not payload or payload == "" then
  return redis.error_reply("TERMINALPAYLOADMISSING")
end
local event_id = redis.call("HGET", KEYS[1], "terminal_event_id") or ""
if event_id ~= "" and #redis.call("XRANGE", KEYS[2], event_id, event_id, "COUNT", 1) == 1 then
  return 0
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
if not expires or expires <= now_millis then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
local id = redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[2], "*", "n", ARGV[1], "p", payload)
redis.call("PEXPIREAT", KEYS[2], expires)
redis.call("HSET", KEYS[1], "terminal_event_id", id)
return 1
`)
	settleLostClaimScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("ZREM", KEYS[5], KEYS[1])
  redis.call("HDEL", KEYS[6], KEYS[1])
  return 0
end
if redis.call("HGET", KEYS[1], "terminal") == "1" then
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("ZREM", KEYS[5], KEYS[1])
  redis.call("HDEL", KEYS[6], KEYS[1])
  return 0
end
local dispatch_token = redis.call("HGET", KEYS[1], "dispatch_provider_token") or ""
local dispatch_lease = redis.call("HGET", KEYS[1], "dispatch_provider_lease") or ""
if dispatch_token == "" or dispatch_lease == "" then
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("ZREM", KEYS[5], KEYS[1])
  redis.call("HDEL", KEYS[6], KEYS[1])
  return 0
end
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
local execution_deadline = tonumber(redis.call("HGET", KEYS[1], "execution_deadline_unix_milli"))
if not execution_deadline then
  return redis.error_reply("CALLADMISSIONCHANGED")
end
local settlement_cause = ARGV[3]
if now_millis < execution_deadline then
  local raw = redis.call("HGET", KEYS[3], redis.call("HGET", KEYS[1], "catalog_field"))
  if raw then
    local entry = cjson.decode(raw)
    local lease = entry.provider_leases[dispatch_lease]
    if entry.registration_token == dispatch_token
    and lease
    and tonumber(lease.expires_at_unix_milli) > now_millis then
      redis.call(
        "ZADD",
        KEYS[4],
        math.min(execution_deadline, tonumber(lease.expires_at_unix_milli)),
        KEYS[1]
      )
      return 0
    end
  end
  settlement_cause = "provider_lease_lost"
end
local payload = redis.call("HGET", KEYS[1], "outcome_unknown_payload")
if not payload or payload == "" then
  return redis.error_reply("OUTCOMEUNKNOWNPAYLOADMISSING")
end
local expires = tonumber(redis.call("HGET", KEYS[1], "expires_at_unix_milli"))
if not expires or expires <= now_millis then
  redis.call("ZREM", KEYS[4], KEYS[1])
  redis.call("ZREM", KEYS[5], KEYS[1])
  redis.call("HDEL", KEYS[6], KEYS[1])
  return 0
end
local digest = redis.sha1hex(payload)
local id = redis.call("XADD", KEYS[2], "MAXLEN", "=", ARGV[2], "*", "n", ARGV[1], "p", payload)
redis.call("PEXPIREAT", KEYS[2], expires)
redis.call(
  "HSET", KEYS[1],
  "terminal", "1",
  "terminal_event_id", id,
  "terminal_digest", digest,
  "terminal_payload", payload,
  "terminal_cause", settlement_cause
)
redis.call("ZREM", KEYS[4], KEYS[1])
redis.call("ZREM", KEYS[5], KEYS[1])
redis.call("HDEL", KEYS[6], KEYS[1])
return 1
`)
	cleanupSettlementMemberScript = redis.NewScript(`
local lease_index = redis.call("HGET", KEYS[2], ARGV[1])
if not lease_index or lease_index == "" then
  lease_index = ARGV[2]
end
redis.call("ZREM", KEYS[1], ARGV[1])
if lease_index and lease_index ~= "" then
  redis.call("ZREM", lease_index, ARGV[1])
end
redis.call("HDEL", KEYS[2], ARGV[1])
return 0
`)
)

// newCallAdmissionStore creates the registry-owned call publication store.
func newCallAdmissionStore(redisClient *redis.Client, registryName string) *callAdmissionStore {
	return &callAdmissionStore{
		redis:          redisClient,
		prefix:         "registry:" + registryName + ":call:",
		catalogHashKey: "map:" + registryName + ":toolsets:content",
		settlementKey:  "registry:" + registryName + ":claimed-call-settlement",
		membershipKey:  "registry:" + registryName + ":claimed-call-membership",
	}
}

// InitializeResultStream creates one bounded initialization event after the
// request publication commits and only while no provider output exists.
// Concurrent callers and overload retries cannot append another event.
func (s *callAdmissionStore) InitializeResultStream(
	ctx context.Context,
	admission callAdmission,
	resultStreamID string,
) error {
	_, err := initializeResultStreamScript.Run(
		ctx,
		s.redis,
		[]string{admission.key, pulseStreamKeyPrefix + resultStreamID},
		admission.digest,
		"init",
		[]byte("{}"),
		toolregistry.ResultStreamMaxLen,
	).Int()
	if err != nil {
		if redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED") {
			return errCallAdmissionNotFound
		}
		return fmt.Errorf("initialize result stream: %w", err)
	}
	return nil
}

// Complete atomically appends one terminal result and commits terminal state.
// Ordinary completion uses the call token for provider authorization; stale
// rejection supplies a distinct current provider token.
func (s *callAdmissionStore) Complete(
	ctx context.Context,
	toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
	providerLease, requestEventID, resultStreamID string,
	payload []byte,
) error {
	key := s.callKey(toolUseID)
	digest := sha256.Sum256(payload)
	_, err := completeCallAdmissionScript.Run(
		ctx,
		s.redis,
		[]string{
			key,
			pulseStreamKeyPrefix + resultStreamID,
			s.catalogHashKey,
			s.settlementKey,
			s.leaseSettlementKey(providerRegistrationToken, providerLease),
			s.membershipKey,
		},
		toolUseID,
		callRegistrationToken,
		toolsetCatalogKey(toolset),
		providerRegistrationToken,
		providerLease,
		requestEventID,
		hex.EncodeToString(digest[:]),
		toolregistry.ResultEventKey,
		payload,
		toolregistry.ResultStreamMaxLen,
	).Slice()
	if err != nil {
		switch {
		case redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED"):
			return errCallAdmissionNotFound
		case redis.HasErrorPrefix(err, "CALLCLAIMCHANGED"):
			return errCallAdmissionConflict
		case redis.HasErrorPrefix(err, "DISPATCHCLAIMCHANGED"):
			return errCallAdmissionConflict
		case redis.HasErrorPrefix(err, "PROVIDERLEASECHANGED"):
			return errToolsetNotFound
		case redis.HasErrorPrefix(err, "TERMINALCONFLICT"):
			return errCallTerminalConflict
		default:
			return fmt.Errorf("complete call admission: %w", err)
		}
	}
	return nil
}

// Claim atomically grants immutable dispatch ownership or returns the
// authoritative non-execution disposition. Stale unclaimed calls receive their
// canonical terminal event at the same linearization point.
func (s *callAdmissionStore) Claim(
	ctx context.Context,
	toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
	providerLease, requestEventID, resultStreamID string,
	stalePayload []byte,
) (callClaimDisposition, error) {
	key := s.callKey(toolUseID)
	digest := sha256.Sum256(stalePayload)
	status, err := claimCallAdmissionScript.Run(
		ctx,
		s.redis,
		[]string{
			key,
			s.catalogHashKey,
			pulseStreamKeyPrefix + resultStreamID,
			s.settlementKey,
			s.leaseSettlementKey(providerRegistrationToken, providerLease),
			s.membershipKey,
		},
		toolUseID,
		callRegistrationToken,
		toolsetCatalogKey(toolset),
		providerRegistrationToken,
		providerLease,
		requestEventID,
		toolregistry.ResultEventKey,
		stalePayload,
		hex.EncodeToString(digest[:]),
		toolregistry.ResultStreamMaxLen,
	).Int()
	if err != nil {
		switch {
		case redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED"):
			return "", errCallAdmissionNotFound
		case redis.HasErrorPrefix(err, "CALLCLAIMCHANGED"):
			return "", errCallAdmissionConflict
		case redis.HasErrorPrefix(err, "PROVIDERLEASECHANGED"):
			return "", errToolsetNotFound
		default:
			return "", fmt.Errorf("claim call admission: %w", err)
		}
	}
	switch status {
	case 1:
		return callClaimExecute, nil
	case 2:
		return callClaimTerminal, nil
	case 3:
		return callClaimClaimed, nil
	case 4:
		return callClaimExpired, nil
	default:
		return "", fmt.Errorf("claim call admission returned invalid status %d", status)
	}
}

// PublishLiveEvent atomically appends a provider event only while the exact
// claimed call remains nonterminal and retained.
func (s *callAdmissionStore) PublishLiveEvent(
	ctx context.Context,
	toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
	providerLease, requestEventID, resultStreamID, eventName string,
	payload []byte,
) error {
	key := s.callKey(toolUseID)
	_, err := publishLiveCallEventScript.Run(
		ctx,
		s.redis,
		[]string{key, pulseStreamKeyPrefix + resultStreamID, s.catalogHashKey},
		toolUseID,
		callRegistrationToken,
		toolsetCatalogKey(toolset),
		providerRegistrationToken,
		providerLease,
		requestEventID,
		eventName,
		payload,
		toolregistry.MaxToolOutputDeltaCount,
		toolregistry.ResultStreamMaxLen,
	).Int()
	if err != nil {
		switch {
		case redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED"):
			return errCallAdmissionNotFound
		case redis.HasErrorPrefix(err, "CALLCLAIMCHANGED"):
			return errCallAdmissionConflict
		case redis.HasErrorPrefix(err, "DISPATCHCLAIMCHANGED"):
			return errCallAdmissionConflict
		case redis.HasErrorPrefix(err, "PROVIDERLEASECHANGED"):
			return errToolsetNotFound
		default:
			return fmt.Errorf("publish live call event: %w", err)
		}
	}
	return nil
}

// ReportOverload atomically publishes retry control only before dispatch. A
// stale queued generation receives the canonical registry-owned terminal
// instead, while a duplicate already owned elsewhere is a no-op.
func (s *callAdmissionStore) ReportOverload(
	ctx context.Context,
	toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
	providerLease, requestEventID, resultStreamID string,
	overloadPayload, stalePayload []byte,
) error {
	key := s.callKey(toolUseID)
	staleDigest := sha256.Sum256(stalePayload)
	_, err := reportOverloadCallScript.Run(
		ctx,
		s.redis,
		[]string{
			key,
			pulseStreamKeyPrefix + resultStreamID,
			s.catalogHashKey,
			s.settlementKey,
			s.membershipKey,
		},
		toolUseID,
		callRegistrationToken,
		toolsetCatalogKey(toolset),
		providerRegistrationToken,
		providerLease,
		requestEventID,
		toolregistry.ResultEventKey,
		overloadPayload,
		stalePayload,
		hex.EncodeToString(staleDigest[:]),
		toolregistry.ProviderOverloadRetryAfter.Milliseconds(),
		toolregistry.ResultStreamMaxLen,
	).Int()
	if err != nil {
		switch {
		case redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED"):
			return errCallAdmissionNotFound
		case redis.HasErrorPrefix(err, "CALLCLAIMCHANGED"):
			return errCallAdmissionConflict
		case redis.HasErrorPrefix(err, "PROVIDERLEASECHANGED"):
			return errToolsetNotFound
		default:
			return fmt.Errorf("report overloaded call admission: %w", err)
		}
	}
	return nil
}

// RestoreTerminal republishes the canonical terminal only when bounded Pulse
// history no longer contains the event recorded by the call record.
func (s *callAdmissionStore) RestoreTerminal(
	ctx context.Context,
	admission callAdmission,
	resultStreamID string,
) error {
	if !admission.terminal {
		return nil
	}
	_, err := restoreCallTerminalScript.Run(
		ctx,
		s.redis,
		[]string{admission.key, pulseStreamKeyPrefix + resultStreamID},
		toolregistry.ResultEventKey,
		toolregistry.ResultStreamMaxLen,
	).Int()
	if err != nil {
		if redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED") {
			return errCallAdmissionNotFound
		}
		return fmt.Errorf("restore call terminal: %w", err)
	}
	return nil
}

// SettleLostClaims commits outcome_unknown when a claim loses its exact lease
// or reaches execution deadline. The Redis scripts are safe for concurrent
// execution by every registry replica.
func (s *callAdmissionStore) SettleLostClaims(ctx context.Context, limit int64) (int, error) {
	now, err := s.redis.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("read settlement time: %w", err)
	}
	keys, err := s.redis.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     s.settlementKey,
		Start:   "-inf",
		Stop:    strconv.FormatInt(now.UnixMilli(), 10),
		ByScore: true,
		Offset:  0,
		Count:   limit,
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("read due call settlements: %w", err)
	}
	settled := 0
	for _, key := range keys {
		toolUseID, err := s.redis.HGet(ctx, key, "tool_use_id").Result()
		if errors.Is(err, redis.Nil) {
			if cleanupErr := s.cleanupSettlementMember(ctx, key, ""); cleanupErr != nil {
				return settled, cleanupErr
			}
			continue
		}
		if err != nil {
			return settled, fmt.Errorf("read due call identity: %w", err)
		}
		status, err := s.settleLostClaim(ctx, key, toolUseID, "execution_deadline")
		if err != nil {
			return settled, err
		}
		settled += status
	}
	return settled, nil
}

// SettleLostClaimsForLease immediately settles claims indexed under a provider
// lease after the catalog has removed that exact authority.
func (s *callAdmissionStore) SettleLostClaimsForLease(
	ctx context.Context,
	providerRegistrationToken, providerLease string,
) error {
	indexKey := s.leaseSettlementKey(providerRegistrationToken, providerLease)
	keys, err := s.redis.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return fmt.Errorf("read provider claim settlements: %w", err)
	}
	for _, key := range keys {
		toolUseID, err := s.redis.HGet(ctx, key, "tool_use_id").Result()
		if errors.Is(err, redis.Nil) {
			if cleanupErr := s.cleanupSettlementMember(ctx, key, indexKey); cleanupErr != nil {
				return cleanupErr
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("read provider claim identity: %w", err)
		}
		if _, err := s.settleLostClaim(ctx, key, toolUseID, "provider_lease_released"); err != nil {
			return err
		}
	}
	return nil
}

// callKey derives the one authoritative Redis record for a global transport
// identity. Keeping the provider token out of the key lets an unpublished call
// move to the current provider without creating a second call record.
func (s *callAdmissionStore) callKey(toolUseID string) string {
	sum := sha256.Sum256([]byte(toolUseID))
	return s.prefix + hex.EncodeToString(sum[:])
}

// leaseSettlementKey indexes every live dispatch owned by one exact provider
// lease so explicit release can complete its calls immediately.
func (s *callAdmissionStore) leaseSettlementKey(providerRegistrationToken, providerLease string) string {
	return s.settlementKey + ":lease:" + providerRegistrationToken + ":" + providerLease
}

// settleLostClaim runs the atomic lease-check and terminal transition for one
// indexed call.
func (s *callAdmissionStore) settleLostClaim(
	ctx context.Context,
	key, toolUseID, cause string,
) (int, error) {
	leaseIndex, err := s.redis.HGet(ctx, s.membershipKey, key).Result()
	if err != nil {
		return 0, fmt.Errorf("read dispatch settlement membership: %w", err)
	}
	status, err := settleLostClaimScript.Run(
		ctx,
		s.redis,
		[]string{
			key,
			pulseStreamKeyPrefix + toolregistry.ResultStreamID(toolUseID),
			s.catalogHashKey,
			s.settlementKey,
			leaseIndex,
			s.membershipKey,
		},
		toolregistry.ResultEventKey,
		toolregistry.ResultStreamMaxLen,
		cause,
	).Int()
	if err != nil {
		return 0, fmt.Errorf("settle lost claim: %w", err)
	}
	return status, nil
}

// cleanupSettlementMember removes every index entry for a call whose
// authoritative hash is already missing.
func (s *callAdmissionStore) cleanupSettlementMember(
	ctx context.Context,
	key, fallbackLeaseIndex string,
) error {
	if _, err := cleanupSettlementMemberScript.Run(
		ctx,
		s.redis,
		[]string{s.settlementKey, s.membershipKey},
		key,
		fallbackLeaseIndex,
	).Int(); err != nil {
		return fmt.Errorf("clean missing call settlement indexes: %w", err)
	}
	return nil
}

// redisResultInt64 normalizes Lua integer replies returned by go-redis.
func redisResultInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("unexpected Redis integer reply %T", value)
	}
}
