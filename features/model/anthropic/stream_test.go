package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"goa.design/goa-ai/runtime/agent/model"
)

// testDecoder feeds a fixed sequence of events to the ssestream.Stream.
type testDecoder struct {
	events []ssestream.Event
	i      int
	err    error
}

func (d *testDecoder) Event() ssestream.Event { return d.events[d.i-1] }

func (d *testDecoder) Next() bool {
	if d.err != nil {
		return false
	}
	if d.i >= len(d.events) {
		return false
	}
	d.i++
	return true
}

func (d *testDecoder) Close() error { return nil }
func (d *testDecoder) Err() error   { return d.err }

func TestAnthropicStreamer_TextAndToolCall(t *testing.T) {
	// Build a minimal text delta and tool_use JSON sequence.
	textDelta := sdk.MessageStreamEventUnion{
		Type: "content_block_delta",
	}
	if err := json.Unmarshal([]byte(`{
  "type": "content_block_delta",
  "index": 0,
  "delta": { "type": "text_delta", "text": "hello" }
}`), &textDelta); err != nil {
		t.Fatalf("unmarshal text delta: %v", err)
	}

	toolStart := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "content_block_start",
  "index": 1,
  "content_block": { "type": "tool_use", "id": "t1", "name": "tool_a" }
}`), &toolStart); err != nil {
		t.Fatalf("unmarshal tool start: %v", err)
	}

	toolDelta := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "content_block_delta",
  "index": 1,
  "delta": { "type": "input_json_delta", "partial_json": "{\"x\":1}" }
}`), &toolDelta); err != nil {
		t.Fatalf("unmarshal tool delta: %v", err)
	}

	toolStop := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "content_block_stop",
  "index": 1
}`), &toolStop); err != nil {
		t.Fatalf("unmarshal tool stop: %v", err)
	}

	stop := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "message_stop"
}`), &stop); err != nil {
		t.Fatalf("unmarshal message stop: %v", err)
	}

	events := []ssestream.Event{
		{Type: "content_block_delta", Data: mustJSON(textDelta)},
		{Type: "content_block_start", Data: mustJSON(toolStart)},
		{Type: "content_block_delta", Data: mustJSON(toolDelta)},
		{Type: "content_block_stop", Data: mustJSON(toolStop)},
		{Type: "message_stop", Data: mustJSON(stop)},
	}

	dec := &testDecoder{events: events}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)
	nameMap := map[string]string{"tool_a": "toolset.tool"}

	s := newAnthropicStreamer(context.Background(), stream, nameMap)
	defer func() {
		_ = s.Close()
	}()

	var chunks []model.Chunk
	for {
		ch, err := s.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unexpected context error: %v", err)
			}
			break
		}
		chunks = append(chunks, ch)
	}

	if len(chunks) == 0 {
		t.Fatalf("expected chunks, got none")
	}

	var sawText, sawTool bool
	for _, ch := range chunks {
		switch ch.Type {
		case model.ChunkTypeText:
			sawText = true
		case model.ChunkTypeToolCall:
			sawTool = true
			if ch.ToolCall == nil {
				t.Fatalf("tool chunk missing ToolCall")
			}
			if string(ch.ToolCall.Name) != "toolset.tool" {
				t.Fatalf("unexpected tool name %q", ch.ToolCall.Name)
			}
		}
	}
	if !sawText {
		t.Fatalf("expected text chunk")
	}
	if !sawTool {
		t.Fatalf("expected tool_call chunk")
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
