package stream

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
)

type mockSink struct {
	events []Event
	err    error
}

func (m *mockSink) Send(ctx context.Context, evt Event) error {
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, evt)
	return nil
}

func (m *mockSink) Close(ctx context.Context) error { return nil }

func TestStreamSubscriber(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewAssistantMessageEvent("r1", "agent1", "hello", nil)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventAssistantReply, sink.events[0].Type())
	v, ok := sink.events[0].(AssistantReply)
	require.True(t, ok)
	require.Equal(t, "hello", v.Data.Text)
}

func TestStreamSubscriber_ToolStart(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewToolCallScheduledEvent("r1", "agent1", "svc.tool", "call-1", json.RawMessage(`{"q":1}`), "queue", "", 0)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolStart, sink.events[0].Type())
	_, ok := sink.events[0].(ToolStart)
	require.True(t, ok)
}

func TestStreamSubscriber_ToolUpdate(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewToolCallUpdatedEvent("r1", "agent1", "parent-1", 3)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolUpdate, sink.events[0].Type())
	upd, ok := sink.events[0].(ToolUpdate)
	require.True(t, ok)
	require.Equal(t, "parent-1", upd.Data.ToolCallID)
	require.Equal(t, 3, upd.Data.ExpectedChildrenTotal)
}

func TestStreamSubscriber_ThinkingBlock_StructuredFinalHasNoDelta(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	// Structured provider reasoning block with final=true and signature populated.
	evt := hooks.NewThinkingBlockEvent("run-1", "agent-1", "full reasoning text", "sig-123", nil, 0, true)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	th, ok := sink.events[0].(PlannerThought)
	require.True(t, ok)
	require.Equal(t, EventPlannerThought, th.Type())
	// Full text and signature preserved for replay.
	require.Equal(t, "full reasoning text", th.Data.Text)
	require.Equal(t, "sig-123", th.Data.Signature)
	require.True(t, th.Data.Final)
	// Note (streaming delta) must be empty so UIs do not append the full text again.
	require.Empty(t, th.Data.Note)
}

func TestStreamSubscriber_ThinkingBlock_StructuredNonFinalDelta(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	// Non-final unsigned block should be treated as a delta.
	evt := hooks.NewThinkingBlockEvent("run-1", "agent-1", "partial", "", nil, 0, false)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	th, ok := sink.events[0].(PlannerThought)
	require.True(t, ok)
	require.Equal(t, EventPlannerThought, th.Type())
	require.Equal(t, "partial", th.Data.Text)
	require.False(t, th.Data.Final)
	// Delta propagated via Note for streaming.
	require.Equal(t, "partial", th.Data.Note)
}
