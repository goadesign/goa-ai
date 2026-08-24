// Package registry makes each routed tool-use identity choose exactly one
// request or rejection across all registry replicas. Before publication, the
// admitted request may move to the current healthy provider. Publication makes
// that assignment permanent; exact retries then observe it until it expires.
package registry

import (
	"context"
	"crypto/sha1" //nolint:gosec // Redis SHA-1 is a retained-record wire checksum, not a security primitive.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// callRejection is the stable typed response replayed for one rejected call.
	callRejection struct {
		kind    callRejectionKind
		message string
	}

	// callRejectedError reports the durable negative decision that prevented
	// admission for one tool-use identity.
	callRejectedError struct {
		rejection callRejection
	}

	// callRejectionKind is the closed set of registry errors that are safe to
	// replay because the rejected decision prevents provider execution.
	callRejectionKind string
)

const (
	callRejectionNotFound     callRejectionKind = "not_found"
	callRejectionValidation   callRejectionKind = "validation_error"
	callRejectionUnavailable  callRejectionKind = "call_not_admitted"
	callTerminalCauseProvider                   = "provider"
)

var (
	attachCallAdmissionScript = redis.NewScript(`
local function canonical_uint(raw)
  if not raw or not string.match(raw, "^%d+$") then
    return nil
  end
  if string.len(raw) > 1 and string.sub(raw, 1, 1) == "0" then
    return nil
  end
  local value = tonumber(raw)
  if not value or value > 9007199254740991 then
    return nil
  end
  return value
end
local function canonical_stream_component(raw)
  if not raw or not string.match(raw, "^%d+$") then
    return false
  end
  if string.len(raw) > 1 and string.sub(raw, 1, 1) == "0" then
    return false
  end
  if string.len(raw) > 20
  or (string.len(raw) == 20 and raw > "18446744073709551615") then
    return false
  end
  return true
end
local function stream_id(raw)
  if not raw then
    return false
  end
  local milliseconds, sequence = string.match(raw, "^(%d+)%-(%d+)$")
  if not canonical_stream_component(milliseconds)
  or not canonical_stream_component(sequence)
  or (milliseconds == "0" and sequence == "0") then
    return false
  end
  return true
end
local function decimal_at_or_before(left, right)
  if string.len(left) ~= string.len(right) then
    return string.len(left) < string.len(right)
  end
  return left <= right
end
local function stream_id_at_or_before(left, right)
  local left_ms, left_seq = string.match(left, "^(%d+)%-(%d+)$")
  local right_ms, right_seq = string.match(right, "^(%d+)%-(%d+)$")
  if left_ms ~= right_ms then
    return decimal_at_or_before(left_ms, right_ms)
  end
  return decimal_at_or_before(left_seq, right_seq)
end
local function admitted_state_error(key, expected_tool_use_id, expected_catalog_field)
  local required_fields = {
    "tool_use_id",
    "registration_token",
    "execution_deadline_unix_milli",
    "expires_at_unix_milli",
    "published",
    "overload",
    "terminal",
    "terminal_event_id",
    "terminal_digest",
    "terminal_payload",
    "terminal_cause",
    "outcome_unknown_payload",
    "catalog_field",
    "output_delta_count",
    "overload_event_id",
    "overload_retry_after_ms",
    "dispatch_provider_token",
    "dispatch_provider_lease",
    "dispatch_request_event_id"
  }
  for _, field in ipairs(required_fields) do
    if redis.call("HEXISTS", key, field) == 0 then
      return "CALLDECISIONINVALID missing admitted field " .. field
    end
  end
  if redis.call("HGET", key, "tool_use_id") ~= expected_tool_use_id then
    return "CALLDECISIONINVALID tool-use identity"
  end
  local registration_token = redis.call("HGET", key, "registration_token")
  if not registration_token
  or string.len(registration_token) ~= 64
  or not string.match(registration_token, "^[0-9a-f]+$") then
    return "CALLDECISIONINVALID registration token"
  end
  local execution_deadline = canonical_uint(redis.call("HGET", key, "execution_deadline_unix_milli"))
  local expires = canonical_uint(redis.call("HGET", key, "expires_at_unix_milli"))
  if not execution_deadline or not expires or expires <= execution_deadline then
    return "CALLDECISIONINVALID deadlines"
  end
  if redis.call("PTTL", key) <= 0 then
    return "CALLDECISIONINVALID expiration"
  end
  local published = redis.call("HGET", key, "published")
  local terminal = redis.call("HGET", key, "terminal")
  if (published ~= "0" and published ~= "1")
  or (terminal ~= "0" and terminal ~= "1") then
    return "CALLDECISIONINVALID state"
  end
  local publication_event_id = redis.call("HGET", key, "publication_event_id") or ""
  local terminal_event_id = redis.call("HGET", key, "terminal_event_id") or ""
  local terminal_digest = redis.call("HGET", key, "terminal_digest") or ""
  local terminal_payload = redis.call("HGET", key, "terminal_payload") or ""
  local terminal_cause = redis.call("HGET", key, "terminal_cause") or ""
  if published == "0" and publication_event_id ~= "" then
    return "CALLDECISIONINVALID publication state"
  end
  if published == "1"
  and (publication_event_id == ""
    or not stream_id(publication_event_id)
    or redis.call("HEXISTS", key, "claim:" .. publication_event_id) == 0) then
    return "CALLDECISIONINVALID publication state"
  end
  if terminal == "0"
  and (terminal_event_id ~= ""
    or terminal_digest ~= ""
    or terminal_payload ~= ""
    or terminal_cause ~= "") then
    return "CALLDECISIONINVALID terminal state"
  end
  if terminal == "1"
  and (published ~= "1"
    or terminal_event_id == ""
    or not stream_id(terminal_event_id)
    or terminal_digest == ""
    or terminal_payload == ""
    or terminal_cause == "") then
    return "CALLDECISIONINVALID terminal state"
  end
  if terminal == "1"
  and terminal_cause ~= "provider"
  and terminal_cause ~= "execution_deadline"
  and terminal_cause ~= "stale_admission"
  and terminal_cause ~= "provider_lease_lost"
  and terminal_cause ~= "provider_lease_released" then
    return "CALLDECISIONINVALID terminal cause"
  end
  local catalog_field = redis.call("HGET", key, "catalog_field")
  if catalog_field ~= expected_catalog_field then
    return "CALLDECISIONINVALID routing state"
  end
  local outcome_unknown_payload = redis.call("HGET", key, "outcome_unknown_payload")
  if not outcome_unknown_payload or outcome_unknown_payload == "" then
    return "CALLDECISIONINVALID outcome payload"
  end
  local output_delta_count = canonical_uint(redis.call("HGET", key, "output_delta_count"))
  local overload_retry_after = canonical_uint(redis.call("HGET", key, "overload_retry_after_ms"))
  if not output_delta_count
  or output_delta_count > tonumber(ARGV[4])
  or not overload_retry_after
  or overload_retry_after > tonumber(ARGV[5]) then
    return "CALLDECISIONINVALID counter state"
  end
  local overload_event_id = redis.call("HGET", key, "overload_event_id")
  if (overload_event_id == "" and overload_retry_after ~= 0)
  or (overload_event_id ~= ""
    and (overload_retry_after == 0 or not stream_id(overload_event_id))) then
    return "CALLDECISIONINVALID overload state"
  end
  local overload_request = redis.call("HGET", key, "overload")
  if overload_request ~= ""
  and (not stream_id(overload_request)
    or overload_event_id == ""
    or not stream_id_at_or_before(overload_request, overload_event_id)) then
    return "CALLDECISIONINVALID overload state"
  end
  local dispatch_token = redis.call("HGET", key, "dispatch_provider_token")
  local dispatch_lease = redis.call("HGET", key, "dispatch_provider_lease")
  local dispatch_request = redis.call("HGET", key, "dispatch_request_event_id")
  local dispatch_expires = redis.call("HGET", key, "dispatch_lease_expires_at_unix_milli")
  local dispatch_expires_value = canonical_uint(dispatch_expires)
  if published == "0"
  and (overload_request ~= "" or overload_event_id ~= "" or dispatch_token ~= "") then
    return "CALLDECISIONINVALID publication state"
  end
  if dispatch_token == "" then
    if dispatch_lease ~= ""
    or dispatch_request ~= ""
    or (dispatch_expires and dispatch_expires ~= "") then
      return "CALLDECISIONINVALID dispatch state"
    end
  elseif dispatch_token ~= registration_token
  or dispatch_lease == ""
  or dispatch_request == ""
  or dispatch_request ~= publication_event_id
  or not dispatch_expires_value
  or dispatch_expires_value == 0 then
    return "CALLDECISIONINVALID dispatch state"
  end
  return nil
end
if redis.call("EXISTS", KEYS[1]) == 0 then
  return {-2, "", "0", "0", "0", "", "", "0", "", "0"}
end
local digest = redis.call("HGET", KEYS[1], "digest")
if digest ~= ARGV[1] then
  return {-1, digest or "", "0", "0", "0", "", "", "0", "", "0"}
end
local decision = redis.call("HGET", KEYS[1], "decision")
if decision == "rejected" then
  if redis.call("PTTL", KEYS[1]) <= 0 then
    return redis.error_reply("CALLDECISIONINVALID rejection expiration")
  end
  return {
    2,
    digest,
    redis.call("HGET", KEYS[1], "kind") or "",
    redis.call("HGET", KEYS[1], "message") or ""
  }
end
if decision ~= "admitted" then
  return redis.error_reply("CALLDECISIONINVALID")
end
local state_error = admitted_state_error(KEYS[1], ARGV[2], ARGV[3])
if state_error then
  return redis.error_reply(state_error)
end
return {
  0,
  digest,
  redis.call("HGET", KEYS[1], "execution_deadline_unix_milli"),
  redis.call("HGET", KEYS[1], "expires_at_unix_milli"),
  redis.call("HGET", KEYS[1], "terminal"),
  redis.call("HGET", KEYS[1], "terminal_event_id"),
  redis.call("HGET", KEYS[1], "registration_token"),
  redis.call("HGET", KEYS[1], "published"),
  redis.call("HGET", KEYS[1], "overload_event_id"),
  redis.call("HGET", KEYS[1], "overload_retry_after_ms"),
  redis.call("HGET", KEYS[1], "outcome_unknown_payload"),
  redis.call("HGET", KEYS[1], "terminal_digest"),
  redis.call("HGET", KEYS[1], "terminal_payload"),
  redis.call("HGET", KEYS[1], "terminal_cause")
}
`)
	ensureCallAdmissionScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  local now = redis.call("TIME")
  local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
  local execution_deadline = now_millis + tonumber(ARGV[4])
  local expires = now_millis + tonumber(ARGV[5])
  redis.call(
    "HSET", KEYS[1],
    "decision", "admitted",
    "digest", ARGV[1],
    "tool_use_id", ARGV[2],
    "registration_token", ARGV[3],
    "execution_deadline_unix_milli", execution_deadline,
    "expires_at_unix_milli", expires,
    "published", "0",
    "overload", "",
    "terminal", "0",
    "terminal_event_id", "",
    "terminal_digest", "",
    "terminal_payload", "",
    "terminal_cause", "",
    "outcome_unknown_payload", ARGV[6],
    "catalog_field", ARGV[7],
    "output_delta_count", "0",
    "overload_event_id", "",
    "overload_retry_after_ms", "0",
    "dispatch_provider_token", "",
    "dispatch_provider_lease", "",
    "dispatch_request_event_id", ""
  )
  redis.call("PEXPIREAT", KEYS[1], expires)
  return {1, ARGV[1], tostring(execution_deadline), tostring(expires), "0", "", ARGV[3], "0", "", "0", ARGV[6], "", "", ""}
end
local digest = redis.call("HGET", KEYS[1], "digest")
if digest ~= ARGV[1] then
  return {-1, digest or ""}
end
local decision = redis.call("HGET", KEYS[1], "decision")
if decision == "rejected" then
  return {
    2,
    digest,
    redis.call("HGET", KEYS[1], "kind") or "",
    redis.call("HGET", KEYS[1], "message") or ""
  }
end
if decision ~= "admitted" then
  return redis.error_reply("CALLDECISIONINVALID")
end
if redis.call("HGET", KEYS[1], "published") == "0"
and redis.call("HGET", KEYS[1], "terminal") == "0" then
  redis.call(
    "HSET", KEYS[1],
    "registration_token", ARGV[3],
    "outcome_unknown_payload", ARGV[6]
  )
end
return {0, digest}
`)
	rejectCallAdmissionScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 1 then
  local digest = redis.call("HGET", KEYS[1], "digest")
  if digest ~= ARGV[1] then
    return {-1, digest or ""}
  end
  local decision = redis.call("HGET", KEYS[1], "decision")
  if decision == "rejected" then
    return {
      1,
      digest,
      redis.call("HGET", KEYS[1], "kind") or "",
      redis.call("HGET", KEYS[1], "message") or ""
    }
  end
  if decision ~= "admitted" then
    return redis.error_reply("CALLDECISIONINVALID")
  end
  if redis.call("HGET", KEYS[1], "published") == "0"
  and redis.call("HGET", KEYS[1], "terminal") == "0" then
    redis.call("DEL", KEYS[1])
  else
    return {0, digest}
  end
end
local now = redis.call("TIME")
local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
local expires = now_millis + tonumber(ARGV[4])
redis.call(
  "HSET", KEYS[1],
  "decision", "rejected",
  "digest", ARGV[1],
  "kind", ARGV[2],
  "message", ARGV[3]
)
redis.call("PEXPIREAT", KEYS[1], expires)
return {1, ARGV[1], ARGV[2], ARGV[3]}
`)
)

// Error describes the durable rejection without exposing Redis state.
func (e *callRejectedError) Error() string {
	return e.rejection.message
}

// Attach loads one existing immutable call decision for exact retry. It never
// creates publication state when retention has expired.
func (s *callAdmissionStore) Attach(
	ctx context.Context,
	toolset, toolUseID, digest string,
) (callAdmission, error) {
	key := s.callKey(toolUseID)
	value, err := attachCallAdmissionScript.Run(
		ctx,
		s.redis,
		[]string{key},
		digest,
		toolUseID,
		toolsetCatalogKey(toolset),
		toolregistry.MaxToolOutputDeltaCount,
		toolregistry.MaxProviderOverloadRetryAfter.Milliseconds(),
	).Slice()
	if err != nil {
		return callAdmission{}, fmt.Errorf("attach call admission: %w", err)
	}
	if len(value) < 2 {
		return callAdmission{}, fmt.Errorf("attach call admission returned %d values", len(value))
	}
	status, err := redisResultInt64(value[0])
	if err != nil {
		return callAdmission{}, err
	}
	existingDigest, _ := value[1].(string)
	switch status {
	case -2:
		return callAdmission{}, errCallAdmissionNotFound
	case -1:
		return callAdmission{}, fmt.Errorf(
			"%w: existing digest %s",
			errCallAdmissionConflict,
			existingDigest,
		)
	case 2:
		rejection, err := parseCallRejection(value[2:])
		if err != nil {
			return callAdmission{}, err
		}
		return callAdmission{}, &callRejectedError{rejection: rejection}
	}
	if status != 0 {
		return callAdmission{}, fmt.Errorf("attach call admission returned invalid status %d", status)
	}
	admission, err := s.parseResult(toolset, key, digest, toolUseID, value)
	if err != nil {
		return callAdmission{}, err
	}
	return admission, nil
}

// Ensure atomically creates or attaches to one call decision. Before initial
// publication, a concurrent caller may move the decision to the currently
// healthy provider because Redis still proves that no external effect began.
// Published decisions remain immutable. A rejected decision returns
// callRejectedError instead.
func (s *callAdmissionStore) Ensure(
	ctx context.Context,
	toolset, toolUseID, registrationToken, digest string,
	executionTimeout, ttl time.Duration,
	outcomeUnknownPayload []byte,
) (callAdmission, bool, error) {
	key := s.callKey(toolUseID)
	value, err := ensureCallAdmissionScript.Run(
		ctx,
		s.redis,
		[]string{key},
		digest,
		toolUseID,
		registrationToken,
		executionTimeout.Milliseconds(),
		ttl.Milliseconds(),
		outcomeUnknownPayload,
		toolsetCatalogKey(toolset),
	).Slice()
	if err != nil {
		return callAdmission{}, false, fmt.Errorf("ensure call admission: %w", err)
	}
	if len(value) < 2 {
		return callAdmission{}, false, fmt.Errorf("ensure call admission returned %d values", len(value))
	}
	status, err := redisResultInt64(value[0])
	if err != nil {
		return callAdmission{}, false, err
	}
	existingDigest, _ := value[1].(string)
	if status < 0 {
		return callAdmission{}, false, fmt.Errorf(
			"%w: existing digest %s",
			errCallAdmissionConflict,
			existingDigest,
		)
	}
	if status == 2 {
		_, err := s.Attach(ctx, toolset, toolUseID, digest)
		return callAdmission{}, false, err
	}
	if status != 0 && status != 1 {
		return callAdmission{}, false, fmt.Errorf("ensure call admission returned invalid status %d", status)
	}
	if status == 0 {
		admission, err := s.Attach(ctx, toolset, toolUseID, digest)
		return admission, false, err
	}
	admission, err := s.parseResult(toolset, key, digest, toolUseID, value)
	if err != nil {
		return callAdmission{}, false, err
	}
	return admission, true, nil
}

// Reject atomically fences a tool-use identity from provider execution. An
// admitted but unpublished decision is still safe to reject because Redis
// proves no provider received it. Published decisions win the race and replay
// until the normal call-retention deadline.
func (s *callAdmissionStore) Reject(
	ctx context.Context,
	toolset, toolUseID, digest string,
	rejection callRejection,
	ttl time.Duration,
) (callAdmission, error) {
	key := s.callKey(toolUseID)
	value, err := rejectCallAdmissionScript.Run(
		ctx,
		s.redis,
		[]string{key},
		digest,
		string(rejection.kind),
		rejection.message,
		ttl.Milliseconds(),
	).Slice()
	if err != nil {
		return callAdmission{}, fmt.Errorf("reject call admission: %w", err)
	}
	if len(value) < 2 {
		return callAdmission{}, fmt.Errorf("reject call admission returned %d values", len(value))
	}
	status, err := redisResultInt64(value[0])
	if err != nil {
		return callAdmission{}, err
	}
	existingDigest, _ := value[1].(string)
	if status < 0 {
		return callAdmission{}, fmt.Errorf(
			"%w: existing digest %s",
			errCallAdmissionConflict,
			existingDigest,
		)
	}
	if status == 1 {
		stored, err := parseCallRejection(value[2:])
		if err != nil {
			return callAdmission{}, err
		}
		return callAdmission{}, &callRejectedError{rejection: stored}
	}
	if status != 0 {
		return callAdmission{}, fmt.Errorf("reject call admission returned invalid status %d", status)
	}
	return s.Attach(ctx, toolset, toolUseID, digest)
}

// parseResult decodes the admitted state returned by Ensure and Attach.
func (s *callAdmissionStore) parseResult(
	toolset, key, digest, toolUseID string,
	value []any,
) (callAdmission, error) {
	if len(value) != 14 {
		return callAdmission{}, fmt.Errorf("call admission returned %d values", len(value))
	}
	executionDeadlineRaw, err := redisResultString(value[2], "execution deadline")
	if err != nil {
		return callAdmission{}, err
	}
	executionDeadlineMillis, err := strconv.ParseInt(executionDeadlineRaw, 10, 64)
	if err != nil {
		return callAdmission{}, fmt.Errorf("parse call admission execution deadline: %w", err)
	}
	if executionDeadlineMillis <= 0 ||
		executionDeadlineRaw != strconv.FormatInt(executionDeadlineMillis, 10) {
		return callAdmission{}, fmt.Errorf("call admission returned invalid execution deadline")
	}
	expiresRaw, err := redisResultString(value[3], "expiration")
	if err != nil {
		return callAdmission{}, err
	}
	expiresMillis, err := strconv.ParseInt(expiresRaw, 10, 64)
	if err != nil {
		return callAdmission{}, fmt.Errorf("parse call admission expiration: %w", err)
	}
	if expiresMillis <= executionDeadlineMillis ||
		expiresRaw != strconv.FormatInt(expiresMillis, 10) {
		return callAdmission{}, fmt.Errorf("call admission expiration does not follow execution deadline")
	}
	terminal, err := redisResultBool(value[4], "terminal")
	if err != nil {
		return callAdmission{}, err
	}
	eventID, err := redisResultString(value[5], "terminal event ID")
	if err != nil {
		return callAdmission{}, err
	}
	if terminal != (eventID != "") {
		return callAdmission{}, fmt.Errorf("call admission returned inconsistent terminal event ID")
	}
	registrationToken, err := redisResultString(value[6], "registration token")
	if err != nil {
		return callAdmission{}, err
	}
	if err := toolregistry.ValidateRegistrationToken(registrationToken); err != nil {
		return callAdmission{}, fmt.Errorf("call admission returned invalid registration token: %w", err)
	}
	published, err := redisResultBool(value[7], "published")
	if err != nil {
		return callAdmission{}, err
	}
	overloadEventID, err := redisResultString(value[8], "overload event ID")
	if err != nil {
		return callAdmission{}, err
	}
	overloadRetryRaw, err := redisResultString(value[9], "overload retry delay")
	if err != nil {
		return callAdmission{}, err
	}
	overloadRetryMillis, err := strconv.ParseInt(overloadRetryRaw, 10, 64)
	if err != nil {
		return callAdmission{}, fmt.Errorf("parse overload retry delay: %w", err)
	}
	if overloadRetryMillis < 0 {
		return callAdmission{}, fmt.Errorf("call admission returned negative overload retry delay")
	}
	if overloadRetryRaw != strconv.FormatInt(overloadRetryMillis, 10) ||
		overloadRetryMillis > toolregistry.MaxProviderOverloadRetryAfter.Milliseconds() ||
		(overloadEventID == "") != (overloadRetryMillis == 0) {
		return callAdmission{}, fmt.Errorf("call admission returned inconsistent overload state")
	}
	outcomeUnknownRaw, err := redisResultString(value[10], "outcome unknown payload")
	if err != nil {
		return callAdmission{}, err
	}
	if err := validateOutcomeUnknownPayload(
		[]byte(outcomeUnknownRaw),
		registrationToken,
		toolUseID,
	); err != nil {
		return callAdmission{}, err
	}
	terminalDigest, err := redisResultString(value[11], "terminal digest")
	if err != nil {
		return callAdmission{}, err
	}
	terminalPayload, err := redisResultString(value[12], "terminal payload")
	if err != nil {
		return callAdmission{}, err
	}
	terminalCause, err := redisResultString(value[13], "terminal cause")
	if err != nil {
		return callAdmission{}, err
	}
	if err := validateTerminalPayload(
		terminal,
		[]byte(terminalPayload),
		terminalDigest,
		terminalCause,
		registrationToken,
		toolUseID,
	); err != nil {
		return callAdmission{}, err
	}
	return callAdmission{
		key:                key,
		digest:             digest,
		executionDeadline:  time.UnixMilli(executionDeadlineMillis),
		expiresAt:          time.UnixMilli(expiresMillis),
		published:          published,
		terminal:           terminal,
		terminalEventID:    eventID,
		overloadEventID:    overloadEventID,
		overloadRetryAfter: time.Duration(overloadRetryMillis) * time.Millisecond,
		catalogHashKey:     s.catalogHashKey,
		catalogField:       toolsetCatalogKey(toolset),
		registrationToken:  registrationToken,
	}, nil
}

// validateOutcomeUnknownPayload verifies the exact retained terminal used when
// an admitted provider may have performed an effect before losing its lease.
func validateOutcomeUnknownPayload(payload []byte, registrationToken, toolUseID string) error {
	message, err := decodePersistedToolResult(payload, registrationToken, toolUseID)
	if err != nil {
		return fmt.Errorf("validate call admission outcome unknown payload: %w", err)
	}
	if message.Error == nil ||
		message.Error.Code != toolregistry.ToolErrorCodeOutcomeUnknown ||
		message.Error.Failure.Kind != planner.FailureInternal ||
		message.Error.Failure.Recovery.Action != planner.RecoveryFinish {
		return fmt.Errorf("call admission outcome unknown payload has invalid failure contract")
	}
	return nil
}

// validateTerminalPayload verifies the retained terminal and its integrity
// digest before replay can restore it to the result stream.
func validateTerminalPayload(
	terminal bool,
	payload []byte,
	digest, cause, registrationToken, toolUseID string,
) error {
	if !terminal {
		if len(payload) != 0 || digest != "" || cause != "" {
			return fmt.Errorf("call admission returned terminal data while nonterminal")
		}
		return nil
	}
	message, err := decodePersistedToolResult(payload, registrationToken, toolUseID)
	if err != nil {
		return fmt.Errorf("validate call admission terminal payload: %w", err)
	}
	switch cause {
	case callTerminalCauseProvider:
		if message.Retry != nil {
			return fmt.Errorf("call admission provider terminal contains retry control")
		}
	case "stale_admission":
		if message.Error == nil ||
			message.Error.Code != toolregistry.ToolErrorCodeStaleRegistration ||
			message.Error.Failure.Kind != planner.FailureUnavailable ||
			message.Error.Failure.Recovery.Action != planner.RecoveryReplan {
			return fmt.Errorf("call admission stale terminal has invalid failure contract")
		}
	case "execution_deadline", "provider_lease_lost", "provider_lease_released":
		if message.Error == nil ||
			message.Error.Code != toolregistry.ToolErrorCodeOutcomeUnknown ||
			message.Error.Failure.Kind != planner.FailureInternal ||
			message.Error.Failure.Recovery.Action != planner.RecoveryFinish {
			return fmt.Errorf("call admission uncertain terminal has invalid failure contract")
		}
	default:
		return fmt.Errorf("call admission returned invalid terminal cause %q", cause)
	}
	var expectedDigest string
	if cause == callTerminalCauseProvider || cause == "stale_admission" {
		sum := sha256.Sum256(payload)
		expectedDigest = hex.EncodeToString(sum[:])
	} else {
		expectedDigest = redisTerminalDigest(payload)
	}
	if digest != expectedDigest {
		return fmt.Errorf("call admission terminal digest does not match payload")
	}
	return nil
}

// redisTerminalDigest reproduces Redis's sha1hex wire checksum for terminal
// payloads committed by settlement scripts. It is an integrity check, not a
// cryptographic authenticity mechanism.
func redisTerminalDigest(payload []byte) string {
	sum := sha1.Sum(payload) //nolint:gosec // Must match Redis sha1hex exactly.
	return hex.EncodeToString(sum[:])
}

// decodePersistedToolResult validates the shared terminal envelope and exact
// call identity stored in a call record.
func decodePersistedToolResult(
	payload []byte,
	registrationToken, toolUseID string,
) (toolregistry.ToolResultMessage, error) {
	var message toolregistry.ToolResultMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return toolregistry.ToolResultMessage{}, fmt.Errorf("decode tool result: %w", err)
	}
	if err := toolregistry.ValidateToolResultMessage(message); err != nil {
		return toolregistry.ToolResultMessage{}, err
	}
	if message.RegistrationToken != registrationToken || message.ToolUseID != toolUseID {
		return toolregistry.ToolResultMessage{}, fmt.Errorf("tool result identity does not match admission")
	}
	return message, nil
}

// parseCallRejection validates the closed rejection result returned by the
// atomic decision scripts.
func parseCallRejection(value []any) (callRejection, error) {
	if len(value) != 2 {
		return callRejection{}, fmt.Errorf("call rejection returned %d values", len(value))
	}
	kindRaw, ok := value[0].(string)
	if !ok {
		return callRejection{}, fmt.Errorf("call rejection returned invalid kind %T", value[0])
	}
	kind := callRejectionKind(kindRaw)
	switch kind {
	case callRejectionNotFound, callRejectionValidation, callRejectionUnavailable:
	default:
		return callRejection{}, fmt.Errorf("call rejection returned invalid kind %q", kind)
	}
	message, ok := value[1].(string)
	if !ok || message == "" {
		return callRejection{}, fmt.Errorf("call rejection returned invalid message")
	}
	return callRejection{kind: kind, message: message}, nil
}

// redisResultString requires one exact Redis string reply.
func redisResultString(value any, field string) (string, error) {
	result, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("call admission returned invalid %s %T", field, value)
	}
	return result, nil
}

// redisResultBool decodes the closed binary state used in call hashes.
func redisResultBool(value any, field string) (bool, error) {
	raw, err := redisResultString(value, field)
	if err != nil {
		return false, err
	}
	switch raw {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("call admission returned invalid %s %q", field, raw)
	}
}
