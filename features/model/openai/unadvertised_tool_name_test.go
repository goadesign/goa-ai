// This file verifies that OpenAI response decoding preserves an unadvertised
// function name as typed validation evidence for the caller.

package openai

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type lateErrorOpenAIStream struct {
	events []responses.ResponseStreamEventUnion
	index  int
	err    error
}

type blockingOpenAIStream struct {
	events  []responses.ResponseStreamEventUnion
	index   int
	blocked chan struct{}
	release chan struct{}
}

func (s *lateErrorOpenAIStream) Next() bool {
	if s.index >= len(s.events) {
		return false
	}
	s.index++
	return true
}

func (s *lateErrorOpenAIStream) Current() responses.ResponseStreamEventUnion {
	return s.events[s.index-1]
}

func (s *lateErrorOpenAIStream) Err() error {
	return s.err
}

func (*lateErrorOpenAIStream) Close() error {
	return nil
}

func (s *blockingOpenAIStream) Next() bool {
	if s.index < len(s.events) {
		s.index++
		return true
	}
	close(s.blocked)
	<-s.release
	return false
}

func (s *blockingOpenAIStream) Current() responses.ResponseStreamEventUnion {
	return s.events[s.index-1]
}

func (*blockingOpenAIStream) Err() error {
	return nil
}

func (*blockingOpenAIStream) Close() error {
	return nil
}

func TestCompleteMarksUnadvertisedToolName(t *testing.T) {
	transport := &mockTransport{completeResponse: mustResponse(t, `{
		"model":"gpt-4o",
		"status":"completed",
		"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7},
		"output":[
			{
				"id":"msg_1",
				"type":"message",
				"role":"assistant",
				"status":"completed",
				"content":[{"type":"output_text","text":"partial text","annotations":[],"logprobs":[]}]
			},
			{
				"id":"fc_1",
				"type":"function_call",
				"call_id":"call_1",
				"name":"lookup",
				"arguments":"{}",
				"status":"completed"
			},
			{
				"id":"fc_2",
				"type":"function_call",
				"call_id":"call_2",
				"name":"look_up",
				"arguments":"{\"ignored\":true}",
				"status":"completed"
			}
		]
	}`)}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	response, err := client.Complete(context.Background(), openAIToolRequest())

	assert.Nil(t, response)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	name, ok := model.UnadvertisedToolName(validationErr)
	assert.True(t, ok)
	assert.Equal(t, "look_up", name)
	assert.Equal(t, &model.TokenUsage{
		Model:        "gpt-4o",
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}, validationErr.Usage())
}

func TestCompleteAcceptsExactAdvertisedToolName(t *testing.T) {
	transport := &mockTransport{completeResponse: mustResponse(t, `{
		"status":"completed",
		"output":[{
			"id":"fc_1",
			"type":"function_call",
			"call_id":"call_1",
			"name":"lookup",
			"arguments":"{}",
			"status":"completed"
		}]
	}`)}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    transport,
	})
	require.NoError(t, err)

	response, err := client.Complete(context.Background(), openAIToolRequest())

	require.NoError(t, err)
	require.Len(t, response.ToolCalls(), 1)
	assert.Equal(t, "lookup", string(response.ToolCalls()[0].Name))
}

func TestStreamMarksUnadvertisedToolName(t *testing.T) {
	stream := &mockStream{events: []responses.ResponseStreamEventUnion{
		mustStreamEvent(t, `{
			"type":"response.output_text.delta",
			"sequence_number":1,
			"item_id":"msg_1",
			"output_index":0,
			"content_index":0,
			"delta":"partial text",
			"logprobs":[]
		}`),
		mustStreamEvent(t, `{
			"type":"response.output_item.added",
			"sequence_number":2,
			"output_index":1,
			"item":{
				"id":"fc_1",
				"type":"function_call",
				"call_id":"call_1",
				"name":"lookup",
				"arguments":"",
				"status":"in_progress"
			}
		}`),
		mustStreamEvent(t, `{
			"type":"response.function_call_arguments.delta",
			"sequence_number":3,
			"item_id":"fc_1",
			"output_index":1,
			"delta":"{}"
		}`),
		mustStreamEvent(t, `{
			"type":"response.output_item.done",
			"sequence_number":4,
			"output_index":1,
			"item":{
				"id":"fc_1",
				"type":"function_call",
				"call_id":"call_1",
				"name":"lookup",
				"arguments":"{}",
				"status":"completed"
			}
		}`),
		mustStreamEvent(t, `{
			"type":"response.output_item.added",
			"sequence_number":5,
			"output_index":2,
			"item":{
				"id":"fc_2",
				"type":"function_call",
				"call_id":"call_2",
				"name":"look_up",
				"arguments":"",
				"status":"in_progress"
			}
		}`),
		mustStreamEvent(t, `{
			"type":"response.completed",
			"sequence_number":6,
			"response":{
				"model":"gpt-4o",
				"status":"completed",
				"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7},
				"output":[
					{
						"id":"msg_1",
						"type":"message",
						"role":"assistant",
						"status":"completed",
						"content":[{"type":"output_text","text":"partial text","annotations":[],"logprobs":[]}]
					},
					{
						"id":"fc_1",
						"type":"function_call",
						"call_id":"call_1",
						"name":"lookup",
						"arguments":"{}",
						"status":"completed"
					},
					{
						"id":"fc_2",
						"type":"function_call",
						"call_id":"call_2",
						"name":"look_up",
						"arguments":"{\"ignored\":true}",
						"status":"completed"
					}
				]
			}
		}`),
	}}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)
	streamer, err := client.Stream(context.Background(), openAIToolRequest())
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, streamer.Close())
	}()
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
	assert.Equal(t, "look_up", name)
	assert.Equal(t, &model.TokenUsage{
		Model:        "gpt-4o",
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}, validationErr.Usage())
	assert.Nil(t, streamer.Response())
	require.Len(t, chunks, 2)
	assert.Equal(t, "partial text", chunks[0].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
	valid := chunks[1].(model.ToolCallDeltaChunk).Delta
	assert.Equal(t, "call_1", valid.ID)
	assert.Equal(t, "lookup", valid.Name.String())
}

func TestStreamTransportFailureSupersedesLatchedUnadvertisedName(t *testing.T) {
	stream := &lateErrorOpenAIStream{
		events: []responses.ResponseStreamEventUnion{mustStreamEvent(t, `{
			"type":"response.output_item.added",
			"sequence_number":1,
			"output_index":0,
			"item":{
				"id":"fc_1",
				"type":"function_call",
				"call_id":"call_1",
				"name":"look_up",
				"arguments":"",
				"status":"in_progress"
			}
		}`)},
		err: errors.New("connection lost"),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)
	streamer, err := client.Stream(context.Background(), openAIToolRequest())
	require.NoError(t, err)

	_, err = streamer.Recv()

	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	_, providerFailure := model.AsProviderError(err)
	assert.True(t, providerFailure)
	assert.NoError(t, streamer.Close())
}

func TestStreamCancellationSupersedesLatchedUnadvertisedName(t *testing.T) {
	stream := &blockingOpenAIStream{
		events: []responses.ResponseStreamEventUnion{mustStreamEvent(t, `{
			"type":"response.output_item.added",
			"sequence_number":1,
			"output_index":0,
			"item":{
				"id":"fc_1",
				"type":"function_call",
				"call_id":"call_1",
				"name":"look_up",
				"arguments":"",
				"status":"in_progress"
			}
		}`)},
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	streamer, err := client.Stream(ctx, openAIToolRequest())
	require.NoError(t, err)
	<-stream.blocked
	cancel()
	_, err = streamer.Recv()
	close(stream.release)

	require.ErrorIs(t, err, context.Canceled)
	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	assert.NoError(t, streamer.Close())
}
