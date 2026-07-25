// Bounded toolset-stream publication. The toolset request stream is the call
// queue, so its backlog bound is enforced at the XADD linearization point:
// counting queued work and appending are one Redis operation, which is the
// only way an admitted call can never be silently trimmed away.
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

	// toolsetStreamMaxLen garbage-collects consumed entries. It exceeds the
	// queue bound by an order of magnitude so approximate trimming can only
	// ever remove entries that every consumer group already moved past.
	toolsetStreamMaxLen = 10 * maxQueuedToolCalls

	// pulseStreamKeyPrefix pins Pulse's physical stream key derivation.
	pulseStreamKeyPrefix = "pulse:stream:"
)

// errToolsetQueueFull reports that every retryable publication slot is taken.
var errToolsetQueueFull = errors.New("toolset call queue is full")

// boundedPublishScript atomically measures the largest per-group backlog
// (entries after the group's last delivered ID plus its pending entries) and
// appends the event only under the bound. A zero bound skips the backlog
// check (health pings must flow even when calls are queued). XADD always
// carries the generous MAXLEN so consumed history is trimmed uniformly.
//
// KEYS: [1]=stream data key
// ARGV: [1]=queue bound (0 = unbounded) [2]=maxlen [3]=event name [4]=payload
var boundedPublishScript = redis.NewScript(`
local bound = tonumber(ARGV[1])
if bound > 0 then
    local backlog = redis.call("XLEN", KEYS[1])
    local groups = redis.pcall("XINFO", "GROUPS", KEYS[1])
    if type(groups) == "table" and groups.err then
        if not string.find(groups.err, "no such key", 1, true) then
            return redis.error_reply(groups.err)
        end
        groups = {}
    end
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
local id = redis.call(
    "XADD", KEYS[1], "MAXLEN", "~", ARGV[2], "*", "n", ARGV[3], "p", ARGV[4]
)
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
		toolsetStreamMaxLen,
		eventName,
		payload,
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
