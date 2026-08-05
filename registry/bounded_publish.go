// Package registry implements bounded toolset-stream publication. The toolset
// request stream is the call queue, so its backlog bound is enforced at the
// XADD linearization point: counting queued work and appending are one Redis
// operation, which is the only way an admitted call can never be silently
// trimmed away.
//
// This file intentionally pins two Pulse internals, mirroring the documented
// rmap pins: the stream data key ("pulse:stream:" + name) and the event field
// layout ("n" = event name, "p" = payload). Integration tests fail if a Pulse
// upgrade changes either.
package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	// maxQueuedToolCalls bounds unconsumed plus pending entries per consumer
	// group on one toolset stream. Publication beyond the bound returns
	// errToolsetQueueFull instead of risking approximate MAXLEN trimming of
	// unread calls.
	maxQueuedToolCalls = 1000

	// pulseStreamKeyPrefix pins Pulse's physical stream key derivation.
	pulseStreamKeyPrefix = "pulse:stream:"
)

var (
	// errToolsetQueueFull reports that every retryable publication slot is taken.
	errToolsetQueueFull = errors.New("toolset call queue is full")
	// errRoutingUnavailable reports that the selected admission has no
	// non-draining provider at the publication linearization point.
	errRoutingUnavailable = errors.New("toolset has no routable provider")
)

// boundedPublishScript atomically resolves an optional call-admission
// publication, measures the largest per-group backlog (entries after the
// group's last delivered ID plus its pending entries), and appends the event
// only under the bound. A zero bound skips the backlog check (health pings must
// flow even when calls are queued). After append, MINID trimming uses the
// earliest pending ID or last-delivered ID across every consumer group; it
// never crosses a PEL entry.
//
// KEYS: [1]=stream data key [2]=optional call-admission hash
// [3]=optional catalog content hash
// ARGV: [1]=queue bound (0 = unbounded) [2]=event name [3]=payload
// [4]=admission digest (empty for ordinary publication) [5]=overload event ID
// [6]=catalog field [7]=registration token
var boundedPublishScript = redis.NewScript(`
local admitted = ARGV[4] ~= ""
if admitted then
    if redis.call("HGET", KEYS[2], "digest") ~= ARGV[4] then
        return redis.error_reply("CALLADMISSIONCHANGED")
    end
    local published = redis.call("HGET", KEYS[2], "published")
    local overload = redis.call("HGET", KEYS[2], "overload")
    if redis.call("HGET", KEYS[2], "terminal") == "1" then
        return {2, redis.call("HGET", KEYS[2], "terminal_event_id") or ""}
    end
    if (ARGV[5] == "" and published == "1") or
       (ARGV[5] ~= "" and overload == ARGV[5]) then
        return {2, redis.call("HGET", KEYS[2], "publication_event_id") or ""}
    end
    local execution_deadline = tonumber(redis.call("HGET", KEYS[2], "execution_deadline_unix_milli"))
    local now = redis.call("TIME")
    local now_millis = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
    if not execution_deadline or execution_deadline <= now_millis then
        return redis.error_reply("CALLADMISSIONCHANGED")
    end
    local raw = redis.call("HGET", KEYS[3], ARGV[6])
    if not raw then
        return redis.error_reply("ROUTINGUNAVAILABLE")
    end
    local entry = cjson.decode(raw)
    if entry.state ~= "active" or entry.registration_token ~= ARGV[7] then
        return redis.error_reply("ROUTINGUNAVAILABLE")
    end
    local routable = false
    for _, lease in pairs(entry.provider_leases) do
        if lease.draining ~= true
        and tonumber(lease.expires_at_unix_milli) > now_millis then
            routable = true
            break
        end
    end
    if not routable then
        return redis.error_reply("ROUTINGUNAVAILABLE")
    end
end
local bound = tonumber(ARGV[1])
local groups = redis.pcall("XINFO", "GROUPS", KEYS[1])
if type(groups) == "table" and groups.err then
    if not string.find(groups.err, "no such key", 1, true) then
        return redis.error_reply(groups.err)
    end
    groups = {}
end
if bound > 0 then
    local backlog = redis.call("XLEN", KEYS[1])
    if #groups > 0 then
        backlog = 0
        for _, group in ipairs(groups) do
            local last
            local pending = 0
            for i = 1, #group, 2 do
                if group[i] == "last-delivered-id" then
                    last = group[i + 1]
                elseif group[i] == "pending" then
                    pending = tonumber(group[i + 1])
                end
            end
            local unread = #redis.call(
                "XRANGE", KEYS[1], "(" .. last, "+", "COUNT", bound + 1
            )
            if unread + pending > backlog then
                backlog = unread + pending
            end
        end
    end
    if backlog >= bound then
        return {0, tostring(backlog)}
    end
end
local id = redis.call("XADD", KEYS[1], "*", "n", ARGV[2], "p", ARGV[3])
local function earlier(left, right)
    local left_ms, left_seq = string.match(left, "^(%d+)%-(%d+)$")
    local right_ms, right_seq = string.match(right, "^(%d+)%-(%d+)$")
    left_ms = tonumber(left_ms)
    right_ms = tonumber(right_ms)
    if left_ms ~= right_ms then
        return left_ms < right_ms
    end
    return tonumber(left_seq) < tonumber(right_seq)
end
local trim_id = nil
for _, group in ipairs(groups) do
    local name
    local last
    local pending = 0
    for i = 1, #group, 2 do
        if group[i] == "name" then
            name = group[i + 1]
        elseif group[i] == "last-delivered-id" then
            last = group[i + 1]
        elseif group[i] == "pending" then
            pending = tonumber(group[i + 1])
        end
    end
    local candidate = last
    if pending > 0 then
        local summary = redis.call("XPENDING", KEYS[1], name)
        candidate = summary[2]
    end
    if candidate and (not trim_id or earlier(candidate, trim_id)) then
        trim_id = candidate
    end
end
if trim_id then
    redis.call("XTRIM", KEYS[1], "MINID", "~", trim_id)
end
if admitted then
    redis.call(
        "HSET",
        KEYS[2],
        "published", "1",
        "overload", ARGV[5],
        "publication_event_id", id,
        "claim:" .. id, "1"
    )
end
return {1, id}
`)

// publishBounded appends one event to the toolset stream under the queue
// bound. It returns the event ID, or errToolsetQueueFull when the bound is
// reached (bound zero disables the check).
func publishBounded(
	ctx context.Context,
	rdb *redis.Client,
	streamID string,
	bound int,
	eventName string,
	payload []byte,
) (string, error) {
	raw, err := boundedPublishScript.Run(
		ctx,
		rdb,
		[]string{pulseStreamKeyPrefix + streamID},
		bound,
		eventName,
		payload,
		"",
		"",
		"",
		"",
	).Slice()
	if err != nil {
		return "", fmt.Errorf("bounded publish to %q: %w", streamID, err)
	}
	if len(raw) != 2 {
		return "", fmt.Errorf("bounded publish returned %d values", len(raw))
	}
	status, ok := raw[0].(int64)
	if !ok {
		return "", fmt.Errorf("bounded publish returned invalid status %T", raw[0])
	}
	value, ok := raw[1].(string)
	if !ok {
		return "", fmt.Errorf("bounded publish returned invalid value %T", raw[1])
	}
	if status == 0 {
		return "", fmt.Errorf("%w: %s queued calls at bound %d", errToolsetQueueFull, value, bound)
	}
	return value, nil
}

// publishAdmittedBounded atomically publishes one exact initial or overload
// attempt and commits that publication in its immutable call-admission hash.
// Exact retries return the original event ID without consuming another queue
// slot or appending another request.
func publishAdmittedBounded(
	ctx context.Context,
	rdb *redis.Client,
	streamID string,
	bound int,
	eventName string,
	payload []byte,
	admission callAdmission,
	overloadEventID string,
) (string, error) {
	raw, err := boundedPublishScript.Run(
		ctx,
		rdb,
		[]string{pulseStreamKeyPrefix + streamID, admission.key, admission.catalogHashKey},
		bound,
		eventName,
		payload,
		admission.digest,
		overloadEventID,
		admission.catalogField,
		admission.registrationToken,
	).Slice()
	if err != nil {
		switch {
		case redis.HasErrorPrefix(err, "CALLADMISSIONCHANGED"):
			return "", errors.New("call admission changed before publication")
		case redis.HasErrorPrefix(err, "ROUTINGUNAVAILABLE"):
			return "", errRoutingUnavailable
		}
		return "", fmt.Errorf("bounded admitted publish to %q: %w", streamID, err)
	}
	if len(raw) != 2 {
		return "", fmt.Errorf("bounded admitted publish returned %d values", len(raw))
	}
	status, ok := raw[0].(int64)
	if !ok {
		return "", fmt.Errorf("bounded admitted publish returned invalid status %T", raw[0])
	}
	eventID, ok := raw[1].(string)
	if !ok {
		return "", fmt.Errorf("bounded admitted publish returned invalid event ID %T", raw[1])
	}
	if status == 0 {
		return "", fmt.Errorf("%w: %s queued calls at bound %d", errToolsetQueueFull, eventID, bound)
	}
	if eventID == "" {
		return "", errors.New("bounded admitted publish returned empty event ID")
	}
	return eventID, nil
}
