package inmem

import (
	"context"
	"testing"

	"goa.design/goa-ai/agents/runtime/session"
)

func TestStoreUpsertLoad(t *testing.T) {
	store := New()
	ctx := context.Background()
	run := session.Run{AgentID: "a", RunID: "r", Status: session.StatusRunning, Labels: map[string]string{"foo": "bar"}}
	if err := store.Upsert(ctx, run); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	loaded, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != session.StatusRunning {
		t.Fatalf("unexpected status: %v", loaded.Status)
	}
	loaded.Labels["foo"] = "baz"
	reread, _ := store.Load(ctx, "r")
	if reread.Labels["foo"] != "bar" {
		t.Fatalf("expected defensive copy")
	}
}

func TestStoreReset(t *testing.T) {
	store := New()
	ctx := context.Background()
	if err := store.Upsert(ctx, session.Run{RunID: "r"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	store.Reset()
	if run, _ := store.Load(ctx, "r"); run.RunID != "" {
		t.Fatalf("expected empty run after reset")
	}
}
