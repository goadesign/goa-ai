package inmem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestStoreUpsertLoad(t *testing.T) {
	store := New()
	ctx := context.Background()
	r := run.Record{AgentID: agent.Ident("a"), RunID: "r", Status: run.StatusRunning, Labels: map[string]string{"foo": "bar"}}
	require.NoError(t, store.Upsert(ctx, r))
	loaded, err := store.Load(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, run.StatusRunning, loaded.Status)
	loaded.Labels["foo"] = "baz"
	reread, _ := store.Load(ctx, "r")
	require.Equal(t, "bar", reread.Labels["foo"], "expected defensive copy")
}

func TestStoreReset(t *testing.T) {
	store := New()
	ctx := context.Background()
	require.NoError(t, store.Upsert(ctx, run.Record{RunID: "r"}))
	store.Reset()
	r, _ := store.Load(ctx, "r")
	require.Empty(t, r.RunID, "expected empty record after reset")
}
