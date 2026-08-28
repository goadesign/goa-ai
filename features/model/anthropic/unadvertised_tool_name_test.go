// This file verifies that Anthropic tool-name misses retain the exact returned
// name while the complete response or stream is rejected.

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type lateErrorAnthropicDecoder struct {
	events   []ssestream.Event
	index    int
	err      error
	closeErr error
}

type blockingAnthropicDecoder struct {
	events  []ssestream.Event
	index   int
	blocked chan struct{}
	release chan struct{}
}

func (d *lateErrorAnthropicDecoder) Event() ssestream.Event {
	return d.events[d.index-1]
}

func (d *lateErrorAnthropicDecoder) Next() bool {
	if d.index >= len(d.events) {
		return false
	}
	d.index++
	return true
}

func (d *lateErrorAnthropicDecoder) Close() error {
	return d.closeErr
}

func (d *lateErrorAnthropicDecoder) Err() error {
	return d.err
}

func (d *blockingAnthropicDecoder) Event() ssestream.Event {
	return d.events[d.index-1]
}

func (d *blockingAnthropicDecoder) Next() bool {
	if d.index < len(d.events) {
		d.index++
		return true
	}
	close(d.blocked)
	<-d.release
	return false
}

func (*blockingAnthropicDecoder) Close() error {
	return nil
}

func (*blockingAnthropicDecoder) Err() error {
	return nil
}

func TestTranslateResponseMarksUnadvertisedToolName(t *testing.T) {
	response, err := translateResponse(&sdk.Message{
		StopReason: sdk.StopReasonToolUse,
		Content: []sdk.ContentBlockUnion{
			{Type: "text", Text: "partial text"},
			{Type: "tool_use", ID: "call-1", Name: "catalog_list_items", Input: json.RawMessage(`{}`)},
			{Type: "tool_use", ID: "call-2", Name: "catalog_list_nearby", Input: json.RawMessage(`{"ignored":true}`)},
		},
	}, map[string]string{"catalog_list_items": "catalog.list_items"})

	assert.Nil(t, response)
	name, ok := model.UnadvertisedToolName(err)
	assert.True(t, ok)
	assert.Equal(t, "catalog_list_nearby", name)
}

func TestTranslateResponseAcceptsExactAdvertisedToolName(t *testing.T) {
	response, err := translateResponse(&sdk.Message{
		StopReason: sdk.StopReasonToolUse,
		Content: []sdk.ContentBlockUnion{{
			Type:  "tool_use",
			ID:    "call-1",
			Name:  "catalog_list_items",
			Input: json.RawMessage(`{}`),
		}},
	}, map[string]string{"catalog_list_items": "catalog.list_items"})

	require.NoError(t, err)
	require.Len(t, response.ToolCalls(), 1)
	assert.Equal(t, "catalog.list_items", response.ToolCalls()[0].Name.String())
}

func TestStreamMarksUnadvertisedToolName(t *testing.T) {
	closeErr := errors.New("anthropic close failed")
	request := &model.Request{
		Model:      "test",
		ModelClass: model.ModelClassDefault,
		Tools: []*model.ToolDefinition{{
			Name:  "catalog.list_items",
			Input: mustAnthropicToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
	}
	contract, err := model.NewRequestContract(request)
	require.NoError(t, err)
	rawEvents := []struct {
		eventType string
		data      string
	}{
		{"message_start", `{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial text"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"catalog_list_items","input":{}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-2","name":"catalog_list_nearby","input":{"ignored":true}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":3}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	events := make([]ssestream.Event, len(rawEvents))
	for index, raw := range rawEvents {
		events[index] = ssestream.Event{Type: raw.eventType, Data: []byte(raw.data)}
	}
	providerStream := ssestream.NewStream[sdk.MessageStreamEventUnion](
		&lateErrorAnthropicDecoder{events: events, closeErr: closeErr},
		nil,
	)
	raw := newAnthropicStreamer(
		context.Background(),
		providerStream,
		map[string]string{"catalog_list_items": "catalog.list_items"},
		nil,
		"test",
		model.ModelClassDefault,
		nil,
		contract,
	)
	streamer, err := contract.ValidateStream(raw)
	require.NoError(t, err)
	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if recvErr != nil {
			err = recvErr
			break
		}
		chunks = append(chunks, chunk)
	}

	require.NotErrorIs(t, err, io.EOF)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	name, ok := model.UnadvertisedToolName(validationErr)
	assert.True(t, ok)
	assert.Equal(t, "catalog_list_nearby", name)
	assert.Equal(t, &model.TokenUsage{
		Model:        "test",
		ModelClass:   model.ModelClassDefault,
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}, validationErr.Usage())
	assert.Nil(t, streamer.Response())
	require.Len(t, chunks, 2)
	assert.IsType(t, model.UsageChunk{}, chunks[0])
	assert.Equal(t, "partial text", chunks[1].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
	assert.ErrorIs(t, streamer.Close(), closeErr)
}

func TestStreamTransportFailureSupersedesLatchedUnadvertisedName(t *testing.T) {
	transportErr := errors.New("connection lost")
	events := []ssestream.Event{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`)},
		{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"missing","input":{}}}`)},
	}
	providerStream := ssestream.NewStream[sdk.MessageStreamEventUnion](
		&lateErrorAnthropicDecoder{events: events, err: transportErr},
		nil,
	)
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	streamer := newAnthropicStreamer(
		context.Background(),
		providerStream,
		nil,
		nil,
		"test",
		model.ModelClassDefault,
		nil,
		contract,
	)

	for err == nil {
		_, err = streamer.Recv()
	}

	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	_, providerFailure := model.AsProviderError(err)
	assert.True(t, providerFailure)
	assert.NoError(t, streamer.Close())
}

func TestStreamCancellationSupersedesLatchedUnadvertisedName(t *testing.T) {
	decoder := &blockingAnthropicDecoder{
		events: []ssestream.Event{
			{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`)},
			{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"missing","input":{}}}`)},
		},
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	providerStream := ssestream.NewStream[sdk.MessageStreamEventUnion](decoder, nil)
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	streamer := newAnthropicStreamer(
		ctx,
		providerStream,
		nil,
		nil,
		"test",
		model.ModelClassDefault,
		nil,
		contract,
	)
	<-decoder.blocked
	cancel()
	for err == nil {
		_, err = streamer.Recv()
	}
	close(decoder.release)

	require.ErrorIs(t, err, context.Canceled)
	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	assert.NoError(t, streamer.Close())
}
