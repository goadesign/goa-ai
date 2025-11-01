package inmem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agents/session"
)

func TestStoreUpsertLoad(t *testing.T) {
	store := New()
	ctx := context.Background()
	run := session.Run{AgentID: "a", RunID: "r", Status: session.StatusRunning, Labels: map[string]string{"foo": "bar"}}
	require.NoError(t, store.Upsert(ctx, run))
	loaded, err := store.Load(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, session.StatusRunning, loaded.Status)
	loaded.Labels["foo"] = "baz"
	reread, _ := store.Load(ctx, "r")
	require.Equal(t, "bar", reread.Labels["foo"], "expected defensive copy")
}

func TestStoreReset(t *testing.T) {
	store := New()
	ctx := context.Background()
	require.NoError(t, store.Upsert(ctx, session.Run{RunID: "r"}))
	store.Reset()
	run, _ := store.Load(ctx, "r")
	require.Empty(t, run.RunID, "expected empty run after reset")
}
