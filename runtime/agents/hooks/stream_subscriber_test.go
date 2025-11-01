package hooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agents/stream"
)

type mockSink struct {
	events []stream.Event
	err    error
}

func (m *mockSink) Send(ctx context.Context, evt stream.Event) error {
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, evt)
	return nil
}

func (m *mockSink) Close(ctx context.Context) error { return nil }

func TestStreamSubscriber(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewStreamSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := NewAssistantMessageEvent("r1", "agent1", "hello", nil)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, stream.EventAssistantReply, sink.events[0].Type())
	v, ok := sink.events[0].(stream.AssistantReply)
	require.True(t, ok)
	require.Equal(t, "hello", v.Text)
}

func TestStreamSubscriber_ToolStart(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewStreamSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := NewToolCallScheduledEvent("r1", "agent1", "svc.tool", "call-1", map[string]any{"q": 1}, "queue", "", 0)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, stream.EventToolStart, sink.events[0].Type())
	_, ok := sink.events[0].(stream.ToolStart)
	require.True(t, ok)
}

func TestStreamSubscriber_ToolUpdate(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewStreamSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := NewToolCallUpdatedEvent("r1", "agent1", "parent-1", 3)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, stream.EventToolUpdate, sink.events[0].Type())
	upd, ok := sink.events[0].(stream.ToolUpdate)
	require.True(t, ok)
	require.Equal(t, "parent-1", upd.Data.ToolCallID)
	require.Equal(t, 3, upd.Data.ExpectedChildrenTotal)
}
