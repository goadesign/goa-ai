package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/memory"
)

type recordingMemoryStore struct {
	events []memory.Event
}

func (s *recordingMemoryStore) LoadRun(ctx context.Context, agentID, runID string) (memory.Snapshot, error) {
	return memory.Snapshot{}, nil
}

func (s *recordingMemoryStore) AppendEvents(ctx context.Context, agentID, runID string, events ...memory.Event) error {
	s.events = append(s.events, events...)
	return nil
}

func TestSuppressChildEvents_FiltersChildToolEventsFromMemory(t *testing.T) {
	store := &recordingMemoryStore{}
	rt := New(WithMemoryStore(store))

	ctx := context.Background()
	const runID = "run-1"
	const agentID = "agent-1"
	const parentID = "parent-1"
	const childID = "child-1"

	// Mark the parent tool call as suppressing child events.
	rt.markSuppressedParent(runID, parentID)

	// Child tool call/result associated with the suppressed parent should not
	// reach memory subscribers.
	childPayload := json.RawMessage(`{"q":1}`)
	rt.publishHook(ctx,
		hooks.NewToolCallScheduledEvent(
			runID,
			agentID,
			"svc.child",
			childID,
			childPayload,
			"",
			parentID,
			0,
		),
		nil,
	)
	rt.publishHook(ctx,
		hooks.NewToolResultReceivedEvent(
			runID,
			agentID,
			"svc.child",
			childID,
			parentID,
			map[string]any{"ok": true},
			time.Second,
			nil,
			nil,
		),
		nil,
	)

	// Parent tool call/result (no parent tool call ID) must still be recorded.
	parentPayload := json.RawMessage(`{"q":2}`)
	rt.publishHook(ctx,
		hooks.NewToolCallScheduledEvent(
			runID,
			agentID,
			"svc.parent",
			parentID,
			parentPayload,
			"",
			"",
			0,
		),
		nil,
	)
	rt.publishHook(ctx,
		hooks.NewToolResultReceivedEvent(
			runID,
			agentID,
			"svc.parent",
			parentID,
			"",
			map[string]any{"ok": true},
			2*time.Second,
			nil,
			nil,
		),
		nil,
	)

	// Memory store should see only the parent tool events.
	require.Len(t, store.events, 2)
	require.Equal(t, memory.EventToolCall, store.events[0].Type)
	require.Equal(t, memory.EventToolResult, store.events[1].Type)
}



