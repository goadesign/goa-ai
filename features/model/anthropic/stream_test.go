package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	messageStart := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "message_start",
  "message": {
    "id": "msg_1",
    "type": "message",
    "role": "assistant",
    "content": [],
    "model": "claude-test",
    "stop_reason": null,
    "stop_sequence": null,
    "usage": { "input_tokens": 1, "output_tokens": 0 }
  }
}`), &messageStart); err != nil {
		t.Fatalf("unmarshal message start: %v", err)
	}

	textStart := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "content_block_start",
  "index": 0,
  "content_block": { "type": "text", "text": "" }
}`), &textStart); err != nil {
		t.Fatalf("unmarshal text start: %v", err)
	}

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

	textStop := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "content_block_stop",
  "index": 0
}`), &textStop); err != nil {
		t.Fatalf("unmarshal text stop: %v", err)
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

	messageDelta := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "message_delta",
  "delta": { "stop_reason": "tool_use", "stop_sequence": null },
  "usage": { "output_tokens": 3 }
}`), &messageDelta); err != nil {
		t.Fatalf("unmarshal message delta: %v", err)
	}

	stop := sdk.MessageStreamEventUnion{}
	if err := json.Unmarshal([]byte(`{
  "type": "message_stop"
}`), &stop); err != nil {
		t.Fatalf("unmarshal message stop: %v", err)
	}

	events := []ssestream.Event{
		{Type: "message_start", Data: mustJSON(messageStart)},
		{Type: "content_block_start", Data: mustJSON(textStart)},
		{Type: "content_block_delta", Data: mustJSON(textDelta)},
		{Type: "content_block_stop", Data: mustJSON(textStop)},
		{Type: "content_block_start", Data: mustJSON(toolStart)},
		{Type: "content_block_delta", Data: mustJSON(toolDelta)},
		{Type: "content_block_stop", Data: mustJSON(toolStop)},
		{Type: "message_delta", Data: mustJSON(messageDelta)},
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
		switch actual := ch.(type) {
		case model.TextChunk:
			sawText = true
		case model.ToolCallChunk:
			sawTool = true
			if string(actual.ToolCall.Name) != "toolset.tool" {
				t.Fatalf("unexpected tool name %q", actual.ToolCall.Name)
			}
		}
	}
	require.NotNil(t, s.Response())
	if !sawText {
		t.Fatalf("expected text chunk")
	}
	if !sawTool {
		t.Fatalf("expected tool_call chunk")
	}
}

// TestAnthropicStreamer_MidStream429Classified verifies that an error
// surfaced by the underlying decoder mid-stream (not just at stream
// establishment) is classified through the same status-to-kind table, so
// errors.Is(err, model.ErrRateLimited) succeeds for a real SDK 429 that
// arrives after the stream is already established.
func TestAnthropicStreamer_MidStream429Classified(t *testing.T) {
	dec := &testDecoder{err: &sdk.Error{StatusCode: http.StatusTooManyRequests}}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)

	s := newAnthropicStreamer(context.Background(), stream, nil)
	defer func() { _ = s.Close() }()

	_, err := s.Recv()
	require.ErrorIs(t, err, model.ErrRateLimited)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
}

// TestAnthropicStreamer_ContextCancelPassthrough verifies that a
// context-cancellation error surfaced by the underlying decoder mid-stream
// passes through unclassified (no ProviderError): cancellation is
// consumer-side flow control, not a provider failure.
func TestAnthropicStreamer_ContextCancelPassthrough(t *testing.T) {
	cause := fmt.Errorf("read: %w", context.Canceled)
	dec := &testDecoder{err: cause}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)

	s := newAnthropicStreamer(context.Background(), stream, nil)
	defer func() { _ = s.Close() }()

	_, err := s.Recv()
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, cause, err) // returned unwrapped, exactly as surfaced
	_, ok := model.AsProviderError(err)
	assert.False(t, ok)
}

// TestAnthropicStreamerClassifiesEventlessStreamAsEmptyStream verifies that a
// stream closing before any message starts is classified as a retryable empty
// stream (model.ErrEmptyStream) instead of an opaque protocol error, so retry
// middleware can safely reissue the request.
func TestAnthropicStreamerClassifiesEventlessStreamAsEmptyStream(t *testing.T) {
	dec := &testDecoder{events: nil}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)

	s := newAnthropicStreamer(context.Background(), stream, nil)
	defer func() { _ = s.Close() }()

	_, err := s.Recv()
	require.ErrorIs(t, err, model.ErrEmptyStream)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindUnavailable, pe.Kind())
	assert.True(t, pe.Retryable())
}

// TestAnthropicStreamerClassifiesMessageStopWithoutStartAsEmptyStream verifies
// that a message_stop arriving before message_start carries the empty-stream
// classification: this is the wire shape Anthropic-family models produce when
// they emit an empty completion.
func TestAnthropicStreamerClassifiesMessageStopWithoutStartAsEmptyStream(t *testing.T) {
	var stop sdk.MessageStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(`{"type":"message_stop"}`), &stop))
	events := []ssestream.Event{{Type: "message_stop", Data: mustJSON(stop)}}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](&testDecoder{events: events}, nil)

	s := newAnthropicStreamer(context.Background(), stream, nil)
	defer func() { _ = s.Close() }()

	_, err := s.Recv()
	require.ErrorIs(t, err, model.ErrEmptyStream)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindUnavailable, pe.Kind())
	assert.True(t, pe.Retryable())
}

func TestAnthropicStreamerRejectsMessageStopWithOpenContentBlock(t *testing.T) {
	rawEvents := []struct {
		eventType string
		data      string
	}{
		{
			eventType: "message_start",
			data: `{
				"type":"message_start",
				"message":{
					"id":"msg_1",
					"type":"message",
					"role":"assistant",
					"content":[],
					"model":"claude-test",
					"stop_reason":null,
					"stop_sequence":null,
					"usage":{"input_tokens":1,"output_tokens":0}
				}
			}`,
		},
		{
			eventType: "content_block_start",
			data:      `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			eventType: "message_stop",
			data:      `{"type":"message_stop"}`,
		},
	}
	events := make([]ssestream.Event, len(rawEvents))
	for i, raw := range rawEvents {
		var event sdk.MessageStreamEventUnion
		require.NoError(t, json.Unmarshal([]byte(raw.data), &event))
		events[i] = ssestream.Event{Type: raw.eventType, Data: mustJSON(event)}
	}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](&testDecoder{events: events}, nil)
	s := newAnthropicStreamer(context.Background(), stream, nil)
	defer func() { _ = s.Close() }()

	_, err := s.Recv()

	require.EqualError(t, err, "anthropic stream: message stopped with 1 open content blocks")
}

func TestThinkingBufferFinalizeRequiresCanonicalVariant(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		signature string
		redacted  []byte
		wantErr   string
	}{
		{name: "plaintext", text: "reasoning", signature: "sig"},
		{name: "redacted", redacted: []byte("opaque")},
		{name: "missing signature", text: "reasoning", wantErr: "thinking plaintext is missing provider signature"},
		{name: "missing text", signature: "sig", wantErr: "thinking signature is missing plaintext content"},
		{
			name:      "mixed variants",
			text:      "reasoning",
			signature: "sig",
			redacted:  []byte("opaque"),
			wantErr:   "thinking block contains both redacted and plaintext content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buffer := &thinkingBuffer{
				signature: test.signature,
				redacted:  test.redacted,
			}
			buffer.text.WriteString(test.text)

			part, err := buffer.finalize(3)

			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				require.Nil(t, part)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, part)
			require.Equal(t, 3, part.Index)
		})
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
