package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// testDecoder feeds a fixed sequence of events to the ssestream.Stream.
	testDecoder struct {
		events []ssestream.Event
		i      int
		err    error
	}

	// bedrockEOFDecoder matches the Anthropic SDK's Bedrock decoder, which
	// returns io.EOF after its final AWS event-stream message.
	bedrockEOFDecoder struct {
		testDecoder
	}

	nonIdempotentAnthropicDecoder struct {
		mu         sync.Mutex
		closeErr   error
		closeCalls int
	}
)

func (*nonIdempotentAnthropicDecoder) Event() ssestream.Event { return ssestream.Event{} }
func (*nonIdempotentAnthropicDecoder) Next() bool             { return false }
func (*nonIdempotentAnthropicDecoder) Err() error             { return nil }

func (d *nonIdempotentAnthropicDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeCalls++
	if d.closeCalls > 1 {
		return errors.New("anthropic decoder closed more than once")
	}
	return d.closeErr
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

func (d *bedrockEOFDecoder) Err() error {
	if d.i >= len(d.events) {
		return io.EOF
	}
	return d.err
}

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
    "usage": {
      "input_tokens": 1,
      "output_tokens": 0,
      "cache_read_input_tokens": 2,
      "cache_creation_input_tokens": 3
    }
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
  "content_block": { "type": "tool_use", "id": "t1", "name": "tool_a", "input": {} }
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

	messageDelta := []byte(`{
  "type": "message_delta",
  "delta": { "stop_reason": "tool_use", "stop_sequence": null },
  "usage": { "output_tokens": 3 }
}`)
	usageDelta := []byte(`{
  "type": "message_delta",
  "delta": { "stop_reason": null, "stop_sequence": null },
  "usage": {
    "input_tokens": 4,
    "output_tokens": 1,
    "cache_read_input_tokens": 5,
    "cache_creation_input_tokens": 6
  }
}`)

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
		{Type: "message_delta", Data: usageDelta},
		{Type: "message_delta", Data: messageDelta},
		{Type: "message_stop", Data: mustJSON(stop)},
	}

	dec := &testDecoder{events: events}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)
	nameMap := map[string]string{"tool_a": "toolset.tool"}

	s := newAnthropicStreamer(
		context.Background(),
		stream,
		nameMap,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
	defer func() {
		_ = s.Close()
	}()

	var chunks []model.Chunk
	var recvErr error
	for {
		var chunk model.Chunk
		chunk, recvErr = s.Recv()
		if recvErr != nil {
			break
		}
		chunks = append(chunks, chunk)
	}
	require.ErrorIs(t, recvErr, io.EOF)

	if len(chunks) == 0 {
		t.Fatalf("expected chunks, got none")
	}

	var sawText, sawTool bool
	var emittedUsage model.TokenUsage
	for _, ch := range chunks {
		switch actual := ch.(type) {
		case model.TextChunk:
			sawText = true
		case model.ToolCallChunk:
			sawTool = true
			if string(actual.ToolCall.Name) != "toolset.tool" {
				t.Fatalf("unexpected tool name %q", actual.ToolCall.Name)
			}
			require.JSONEq(t, `{"x":1}`, string(actual.ToolCall.Payload))
		case model.UsageChunk:
			emittedUsage.InputTokens += actual.Usage.InputTokens
			emittedUsage.OutputTokens += actual.Usage.OutputTokens
			emittedUsage.TotalTokens += actual.Usage.TotalTokens
			emittedUsage.CacheReadTokens += actual.Usage.CacheReadTokens
			emittedUsage.CacheWriteTokens += actual.Usage.CacheWriteTokens
		}
	}
	response := s.Response()
	require.NotNil(t, response)
	require.Equal(t, 4, response.Usage.InputTokens)
	require.Equal(t, 3, response.Usage.OutputTokens)
	require.Equal(t, 7, response.Usage.TotalTokens)
	require.Equal(t, 5, response.Usage.CacheReadTokens)
	require.Equal(t, 6, response.Usage.CacheWriteTokens)
	require.Equal(t, response.Usage.InputTokens, emittedUsage.InputTokens)
	require.Equal(t, response.Usage.OutputTokens, emittedUsage.OutputTokens)
	require.Equal(t, response.Usage.TotalTokens, emittedUsage.TotalTokens)
	require.Equal(t, response.Usage.CacheReadTokens, emittedUsage.CacheReadTokens)
	require.Equal(t, response.Usage.CacheWriteTokens, emittedUsage.CacheWriteTokens)
	if !sawText {
		t.Fatalf("expected text chunk")
	}
	if !sawTool {
		t.Fatalf("expected tool_call chunk")
	}
}

func TestAnthropicStreamerTreatsBedrockEOFAsCleanCompletion(t *testing.T) {
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
					"usage":{"input_tokens":3,"output_tokens":0}
				}
			}`,
		},
		{
			eventType: "content_block_start",
			data:      `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			eventType: "content_block_delta",
			data:      `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
		},
		{
			eventType: "content_block_stop",
			data:      `{"type":"content_block_stop","index":0}`,
		},
		{
			eventType: "message_delta",
			data:      `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":3,"output_tokens":1}}`,
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
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](
		&bedrockEOFDecoder{testDecoder: testDecoder{events: events}},
		nil,
	)
	translated := newAnthropicStreamer(
		t.Context(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
	defer func() { require.NoError(t, translated.Close()) }()

	var usageChunks []model.TokenUsage
	for {
		chunk, err := translated.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if usage, ok := chunk.(model.UsageChunk); ok {
			usageChunks = append(usageChunks, usage.Usage)
		}
	}
	response := translated.Response()
	require.NotNil(t, response)
	require.Equal(t, "end_turn", response.StopReason)
	require.Equal(t, []model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.TextPart{Text: "OK"},
		},
	}}, response.Content)
	require.Equal(t, 3, response.Usage.InputTokens)
	require.Equal(t, 1, response.Usage.OutputTokens)
	require.Equal(t, "claude-test", response.Usage.Model)
	require.Equal(t, model.ModelClassDefault, response.Usage.ModelClass)
	require.Len(t, usageChunks, 2)
	for _, usage := range usageChunks {
		require.Equal(t, response.Usage.Model, usage.Model)
		require.Equal(t, response.Usage.ModelClass, usage.ModelClass)
	}
}

func TestAnthropicStreamerValidatesNativeStructuredOutput(t *testing.T) {
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
					"model":"claude-sonnet-5",
					"stop_reason":null,
					"stop_sequence":null,
					"usage":{"input_tokens":0,"output_tokens":0}
				}
			}`,
		},
		{
			eventType: "content_block_start",
			data:      `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			eventType: "content_block_delta",
			data:      `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"answer\":"}}`,
		},
		{
			eventType: "content_block_delta",
			data:      `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"\"yes\"}"}}`,
		},
		{
			eventType: "content_block_stop",
			data:      `{"type":"content_block_stop","index":0}`,
		},
		{
			eventType: "message_delta",
			data:      `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"input_tokens":0,"output_tokens":0}}`,
		},
		{
			eventType: "message_stop",
			data:      `{"type":"message_stop"}`,
		},
	}
	events := make([]ssestream.Event, len(rawEvents))
	for index, raw := range rawEvents {
		var event sdk.MessageStreamEventUnion
		require.NoError(t, json.Unmarshal([]byte(raw.data), &event))
		events[index] = ssestream.Event{Type: raw.eventType, Data: mustJSON(event)}
	}

	output := &model.StructuredOutput{
		Name:   "answer",
		Schema: rawjson.Message(`{"type":"object"}`),
	}
	request := &model.Request{StructuredOutput: output}
	decoded := false
	require.NoError(t, model.SetCompletionValidator(request, func(_ *model.Response, completion *model.Completion) error {
		require.NotNil(t, completion)
		require.JSONEq(t, `{"answer":"yes"}`, string(completion.Payload))
		decoded = true
		return nil
	}))
	contract, err := model.NewRequestContract(request)
	require.NoError(t, err)
	providerStream := ssestream.NewStream[sdk.MessageStreamEventUnion](
		&testDecoder{events: events},
		nil,
	)
	raw := newAnthropicStreamer(
		t.Context(),
		providerStream,
		nil,
		nil,
		"claude-sonnet-5",
		model.ModelClassDefault,
		output,
		contract,
	)
	stream, err := contract.ValidateStream(raw)
	require.NoError(t, err)
	defer func() { require.NoError(t, stream.Close()) }()

	var chunks []model.Chunk
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}

	require.True(t, decoded)
	require.Len(t, chunks, 4)
	assert.Equal(t, `{"answer":`, chunks[0].(model.CompletionDeltaChunk).Delta.Delta)
	assert.Equal(t, `"yes"}`, chunks[1].(model.CompletionDeltaChunk).Delta.Delta)
	assert.JSONEq(t, `{"answer":"yes"}`, string(chunks[2].(model.CompletionChunk).Completion.Payload))
	stop := chunks[3].(model.StopChunk)
	assert.Equal(t, "max_tokens", stop.Reason)
	assert.True(t, stop.OutputLimited)
	response := stream.Response()
	require.NotNil(t, response)
	assert.True(t, response.OutputLimited)
}

// TestAnthropicStreamerRejectsOversizedSDKSnapshotBeforeAccumulation verifies
// one oversized event fails before the SDK response accumulator retains it.
func TestAnthropicStreamerRejectsOversizedSDKSnapshotBeforeAccumulation(t *testing.T) {
	delta := sdk.MessageStreamEventUnion{}
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`,
		strings.Repeat("x", (16<<20)+1),
	)), &delta))
	decoder := &testDecoder{events: []ssestream.Event{{
		Type: "content_block_delta",
		Data: mustJSON(delta),
	}}}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](decoder, nil)
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	validated := newAnthropicStreamer(
		t.Context(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		contract,
	)
	defer func() { require.NoError(t, validated.Close()) }()

	_, err = validated.Recv()

	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, errors.Unwrap(validationErr), "retained output exceeds 16777216 bytes")
	require.True(t, validationErr.Evidence().Present)
}

func TestAnthropicStreamerRejectsMissingToolCallIDWithUsage(t *testing.T) {
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
					"usage":{"input_tokens":7,"output_tokens":2}
				}
			}`,
		},
		{
			eventType: "content_block_start",
			data:      `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"lookup","input":{}}}`,
		},
	}
	events := make([]ssestream.Event, len(rawEvents))
	for i, raw := range rawEvents {
		var event sdk.MessageStreamEventUnion
		require.NoError(t, json.Unmarshal([]byte(raw.data), &event))
		events[i] = ssestream.Event{Type: raw.eventType, Data: mustJSON(event)}
	}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](&testDecoder{events: events}, nil)
	translated := newAnthropicStreamer(
		t.Context(),
		stream,
		map[string]string{"lookup": "svc.lookup"},
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
	defer func() { require.NoError(t, translated.Close()) }()

	_, err := translated.Recv()
	require.NoError(t, err)
	_, err = translated.Recv()

	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, errors.Unwrap(validationErr), "tool use block missing id")
	require.Equal(t, 7, validationErr.Usage().InputTokens)
	require.Equal(t, 2, validationErr.Usage().OutputTokens)
}

// TestAnthropicStreamer_MidStream429Classified verifies that an error
// surfaced by the underlying decoder mid-stream (not just at stream
// establishment) is classified through the same status-to-kind table, so
// errors.Is(err, model.ErrRateLimited) succeeds for a real SDK 429 that
// arrives after the stream is already established.
func TestAnthropicStreamer_MidStream429Classified(t *testing.T) {
	dec := &testDecoder{err: &sdk.Error{StatusCode: http.StatusTooManyRequests}}
	stream := ssestream.NewStream[sdk.MessageStreamEventUnion](dec, nil)

	s := newAnthropicStreamer(
		context.Background(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
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

	s := newAnthropicStreamer(
		context.Background(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
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

	s := newAnthropicStreamer(
		context.Background(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
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

	s := newAnthropicStreamer(
		context.Background(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
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
	s := newAnthropicStreamer(
		context.Background(),
		stream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)
	defer func() { _ = s.Close() }()

	_, err := s.Recv()
	require.NoError(t, err)
	_, err = s.Recv()

	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, errors.Unwrap(validationErr), "anthropic stream: message stopped with 1 open content blocks")
	require.Equal(t, 1, validationErr.Usage().InputTokens)
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
		// Signature-only blocks are canonical for thinking display "omitted"
		// (the Opus 4.8-class default) and must decode as signed empty text.
		{name: "signature only", signature: "sig"},
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

// TestAnthropicChunkProcessorPreservesInitialToolInput verifies that an
// Anthropic tool-use block with no JSON deltas keeps the input carried by its
// start event. No-argument tools still produce the code-owned empty object.
func TestAnthropicChunkProcessorPreservesInitialToolInput(t *testing.T) {
	var messageStart sdk.MessageStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"message_start",
		"message":{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"content":[],
			"model":"claude-test",
			"usage":{"input_tokens":1,"output_tokens":0}
		}
	}`), &messageStart))
	var toolStart sdk.MessageStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"content_block_start",
		"index":0,
		"content_block":{
			"type":"tool_use",
			"id":"toolu_1",
			"name":"continue_abcd",
			"input":{}
		}
	}`), &toolStart))
	var missingInputToolStart sdk.MessageStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"content_block_start",
		"index":0,
		"content_block":{
			"type":"tool_use",
			"id":"toolu_1",
			"name":"continue_abcd"
		}
	}`), &missingInputToolStart))
	var toolStop sdk.MessageStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"content_block_stop",
		"index":0
	}`), &toolStop))

	tests := []struct {
		name              string
		start             sdk.MessageStreamEventUnion
		noArgumentTools   map[string]struct{}
		wantPayload       string
		wantErrorContains string
	}{
		{
			name:            "no-argument tool",
			start:           toolStart,
			noArgumentTools: map[string]struct{}{"catalog.continue_results": {}},
			wantPayload:     `{}`,
		},
		{
			name:        "ordinary tool with empty object",
			start:       toolStart,
			wantPayload: `{}`,
		},
		{
			name:              "ordinary tool without input",
			start:             missingInputToolStart,
			wantErrorContains: "tool payload is not valid JSON",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []model.ToolCall
			processor := newAnthropicChunkProcessor(
				func(chunk model.Chunk) error {
					if call, ok := chunk.(model.ToolCallChunk); ok {
						calls = append(calls, call.ToolCall)
					}
					return nil
				},
				map[string]string{"continue_abcd": "catalog.continue_results"},
				test.noArgumentTools,
				nil,
			)

			require.NoError(t, processor.Handle(messageStart))
			require.NoError(t, processor.Handle(test.start))
			err := processor.Handle(toolStop)
			if test.wantErrorContains != "" {
				require.ErrorContains(t, err, test.wantErrorContains)
				require.Empty(t, calls)
				return
			}
			require.NoError(t, err)
			require.Len(t, calls, 1)
			require.Equal(t, "catalog.continue_results", string(calls[0].Name))
			require.JSONEq(t, test.wantPayload, string(calls[0].Payload))
		})
	}
}

func TestAnthropicChunkProcessorAssignsDenseThinkingIndexes(t *testing.T) {
	var finalIndexes []int
	var finalSignatures []string
	processor := newAnthropicChunkProcessor(func(chunk model.Chunk) error {
		thinking, ok := chunk.(model.ThinkingChunk)
		if !ok {
			return nil
		}
		part := thinking.Message.Parts[0].(model.ThinkingPart)
		if part.Final {
			finalIndexes = append(finalIndexes, part.Index)
			finalSignatures = append(finalSignatures, part.Signature)
		}
		return nil
	}, nil, nil, nil)

	var messageStart sdk.MessageStreamEventUnion
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"message_start",
		"message":{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"content":[],
			"model":"claude-test",
			"usage":{"input_tokens":1,"output_tokens":0}
		}
	}`), &messageStart))
	require.NoError(t, processor.Handle(messageStart))
	for sequence, contentIndex := range []int{2, 7} {
		thinking := ""
		if sequence > 0 {
			thinking = "reasoning"
		}
		var start sdk.MessageStreamEventUnion
		require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{
			"type":"content_block_start",
			"index":%d,
			"content_block":{"type":"thinking","thinking":%q,"signature":"sig-%d"}
		}`, contentIndex, thinking, sequence+1)), &start))
		require.NoError(t, processor.Handle(start))
		var stop sdk.MessageStreamEventUnion
		require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(
			`{"type":"content_block_stop","index":%d}`,
			contentIndex,
		)), &stop))
		require.NoError(t, processor.Handle(stop))
	}

	require.Equal(t, []int{0, 1}, finalIndexes)
	require.Equal(t, []string{"sig-1", "sig-2"}, finalSignatures)
}

func TestAnthropicStreamerClosesProviderStreamOnce(t *testing.T) {
	cleanupErr := errors.New("anthropic cleanup failed")
	decoder := &nonIdempotentAnthropicDecoder{closeErr: cleanupErr}
	providerStream := ssestream.NewStream[sdk.MessageStreamEventUnion](decoder, nil)
	streamer := newAnthropicStreamer(
		t.Context(),
		providerStream,
		nil,
		nil,
		"claude-test",
		model.ModelClassDefault,
		nil,
		anthropicTestContract(t),
	)

	_, recvErr := streamer.Recv()
	require.Error(t, recvErr)
	require.ErrorIs(t, streamer.Close(), cleanupErr)
	require.ErrorIs(t, streamer.Close(), cleanupErr)
	decoder.mu.Lock()
	closeCalls := decoder.closeCalls
	decoder.mu.Unlock()
	require.Equal(t, 1, closeCalls)
}

// anthropicTestContract builds the production request contract required by
// every stream translator test.
func anthropicTestContract(t *testing.T) *model.RequestContract {
	t.Helper()
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	return contract
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
