package inmem

import (
	"context"
	"testing"
	"time"

	"goa.design/goa-ai/agents/runtime/memory"
)

func TestStoreAppendAndLoad(t *testing.T) {
	store := New()
	ctx := context.Background()
	event := memory.Event{Type: memory.EventToolCall, Timestamp: time.Now(), Data: map[string]any{"tool": "foo"}}
	if err := store.AppendEvents(ctx, "agent", "run", event); err != nil {
		t.Fatalf("append: %v", err)
	}
	snap, err := store.LoadRun(ctx, "agent", "run")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(snap.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(snap.Events))
	}
	if snap.Events[0].Type != memory.EventToolCall {
		t.Fatalf("unexpected event type: %v", snap.Events[0].Type)
	}
}

func TestStoreIsolation(t *testing.T) {
	store := New()
	ctx := context.Background()
	first := memory.Event{Type: memory.EventToolCall}
	if err := store.AppendEvents(ctx, "agent", "run", first); err != nil {
		t.Fatalf("append: %v", err)
	}
	snap, err := store.LoadRun(ctx, "agent", "run")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	snap.Events[0].Type = memory.EventToolResult
	snap2, err := store.LoadRun(ctx, "agent", "run")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if snap2.Events[0].Type != memory.EventToolCall {
		t.Fatalf("store mutated by caller")
	}
}
