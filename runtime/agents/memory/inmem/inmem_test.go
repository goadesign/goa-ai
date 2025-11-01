package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agents/memory"
)

func TestStoreAppendAndLoad(t *testing.T) {
	store := New()
	ctx := context.Background()
	event := memory.Event{Type: memory.EventToolCall, Timestamp: time.Now(), Data: map[string]any{"tool": "foo"}}
	require.NoError(t, store.AppendEvents(ctx, "agent", "run", event))
	snap, err := store.LoadRun(ctx, "agent", "run")
	require.NoError(t, err)
	require.Len(t, snap.Events, 1)
	require.Equal(t, memory.EventToolCall, snap.Events[0].Type)
}

func TestStoreIsolation(t *testing.T) {
	store := New()
	ctx := context.Background()
	first := memory.Event{Type: memory.EventToolCall}
	require.NoError(t, store.AppendEvents(ctx, "agent", "run", first))
	snap, err := store.LoadRun(ctx, "agent", "run")
	require.NoError(t, err)
	snap.Events[0].Type = memory.EventToolResult
	snap2, err := store.LoadRun(ctx, "agent", "run")
	require.NoError(t, err)
	require.Equal(t, memory.EventToolCall, snap2.Events[0].Type, "store mutated by caller")
}
