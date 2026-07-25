//go:build integration

package registry

// Bounded publication tests prove on live Redis that an admitted call can
// never be silently trimmed: publication beyond the per-group backlog bound
// is rejected, pings bypass the bound, and consumption reopens the queue.

import (
	"context"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestBoundedPublishRejectsQueueFullAndRecovers(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	const streamID = "toolset:bounded-test:requests"
	const bound = 10
	key := pulseStreamKeyPrefix + streamID

	// With no consumer group, every retained entry is unconsumed backlog.
	for i := 0; i < bound; i++ {
		_, err := publishBounded(ctx, rdb, streamID, bound, "call", []byte(strconv.Itoa(i)))
		require.NoError(t, err)
	}
	_, err := publishBounded(ctx, rdb, streamID, bound, "call", []byte("overflow"))
	require.ErrorIs(t, err, errToolsetQueueFull)

	// Health pings must flow while calls are queued.
	pingID, err := publishBounded(ctx, rdb, streamID, 0, "ping", []byte("{}"))
	require.NoError(t, err)
	require.NotEmpty(t, pingID)

	// A group that consumed all history empties the backlog and reopens
	// publication.
	require.NoError(t, rdb.XGroupCreate(ctx, key, "provider", "$").Err())
	id, err := publishBounded(ctx, rdb, streamID, bound, "call", []byte("reopened"))
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// Delivered-but-unacknowledged entries stay counted: drain the stream
	// into the PEL and verify the backlog still enforces the bound.
	read, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    "provider",
		Consumer: "consumer",
		Streams:  []string{key, ">"},
		Count:    1,
	}).Result()
	require.NoError(t, err)
	require.Len(t, read, 1)
	require.Len(t, read[0].Messages, 1)
	for i := 0; i < bound-1; i++ {
		_, err := publishBounded(ctx, rdb, streamID, bound, "call", []byte("fill"))
		require.NoError(t, err)
	}
	_, err = publishBounded(ctx, rdb, streamID, bound, "call", []byte("overflow"))
	require.ErrorIs(t, err, errToolsetQueueFull)

	// Acknowledging the pending entry frees exactly one slot.
	require.NoError(t, rdb.XAck(ctx, key, "provider", read[0].Messages[0].ID).Err())
	_, err = publishBounded(ctx, rdb, streamID, bound, "call", []byte("freed"))
	require.NoError(t, err)
}
