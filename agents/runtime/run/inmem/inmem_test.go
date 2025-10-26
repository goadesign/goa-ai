package inmem

import (
	"context"
	"testing"

	"goa.design/goa-ai/agents/runtime/run"
)

func TestStoreUpsertLoad(t *testing.T) {
	store := New()
	ctx := context.Background()
	r := run.Record{AgentID: "a", RunID: "r", Status: run.StatusRunning, Labels: map[string]string{"foo": "bar"}}
	if err := store.Upsert(ctx, r); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	loaded, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != run.StatusRunning {
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
	if err := store.Upsert(ctx, run.Record{RunID: "r"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	store.Reset()
	if r, _ := store.Load(ctx, "r"); r.RunID != "" {
		t.Fatalf("expected empty record after reset")
	}
}
