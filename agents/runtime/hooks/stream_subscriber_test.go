package hooks

import (
	"context"
	"testing"

	"goa.design/goa-ai/agents/runtime/stream"
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
	if err != nil {
		t.Fatalf("subscriber: %v", err)
	}
	ctx := context.Background()
	evt := NewAssistantMessageEvent("r1", "agent1", "hello", nil)
	if err := sub.HandleEvent(ctx, evt); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	if sink.events[0].Type != stream.EventAssistantReply {
		t.Fatalf("unexpected stream type: %v", sink.events[0].Type)
	}
	if sink.events[0].Content != "hello" {
		t.Fatalf("unexpected content: %v", sink.events[0].Content)
	}
}
