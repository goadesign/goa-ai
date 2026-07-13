package completion

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type testCompletionResult struct {
	AssistantText string `json:"assistant_text"`
}

func testCompletionSpec() Spec[testCompletionResult] {
	return Spec[testCompletionResult]{
		Name:        "draft_from_transcript",
		Description: "Synthesize task draft",
		Result: tools.TypeSpec{
			Name:   "DraftFromTranscriptResult",
			Schema: tools.RawJSON(`{"type":"object","required":["assistant_text"]}`),
		},
		Codec: tools.JSONCodec[testCompletionResult]{
			ToJSON:   marshalTestCompletionResult,
			FromJSON: unmarshalTestCompletionResult,
		},
	}
}

func marshalTestCompletionResult(value testCompletionResult) ([]byte, error) {
	return json.Marshal(value)
}

func unmarshalTestCompletionResult(data []byte) (testCompletionResult, error) {
	var out testCompletionResult
	err := json.Unmarshal(data, &out)
	return out, err
}

type recordingCompletionClient struct {
	request   *model.Request
	response  *model.Response
	streamer  model.Streamer
	err       error
	streamErr error
}

func (c *recordingCompletionClient) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	c.request = req
	return c.response, c.err
}

func (c *recordingCompletionClient) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	c.request = req
	return c.streamer, c.streamErr
}

type stubStreamer struct{}

func (stubStreamer) Recv() (model.Chunk, error) {
	return nil, io.EOF
}

func (stubStreamer) Close() error {
	return nil
}

func (stubStreamer) Response() *model.Response {
	return nil
}

type recvResult struct {
	chunk model.Chunk
	err   error
}

type scriptedStreamer struct {
	results  []recvResult
	response *model.Response
	index    int
}

func (s *scriptedStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.results) {
		return nil, io.EOF
	}
	result := s.results[s.index]
	s.index++
	return result.chunk, result.err
}

func (s *scriptedStreamer) Close() error {
	return nil
}

func (s *scriptedStreamer) Response() *model.Response {
	return s.response
}

func TestCompleteSetsStructuredOutputAndDecodesTypedValue(t *testing.T) {
	spec := testCompletionSpec()
	client := &recordingCompletionClient{
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{Text: "internal", Final: true},
					model.TextPart{Text: `{"assistant_text":"created a draft"}`},
				},
			}},
			StopReason: "stop",
		},
	}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "create a task"}},
		}},
	}

	resp, err := Complete(context.Background(), client, req, spec)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, testCompletionResult{AssistantText: "created a draft"}, resp.Value)
	require.NotNil(t, client.request)
	require.NotNil(t, client.request.StructuredOutput)
	assert.Equal(t, "draft_from_transcript", client.request.StructuredOutput.Name)
	assert.JSONEq(t, `{"type":"object","required":["assistant_text"]}`, string(client.request.StructuredOutput.Schema))
	assert.Nil(t, req.StructuredOutput)
}

func TestCompleteRejectsStreamingRequests(t *testing.T) {
	_, err := Complete(
		context.Background(),
		&recordingCompletionClient{},
		&model.Request{Stream: true},
		testCompletionSpec(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support streaming")
}

func TestCompleteRejectsToolDefinitions(t *testing.T) {
	_, err := Complete(
		context.Background(),
		&recordingCompletionClient{},
		&model.Request{
			Tools: []*model.ToolDefinition{{
				Name:        "lookup",
				Description: "Search",
				Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
			}},
		},
		testCompletionSpec(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not allow tool definitions")
}

func TestCompleteRejectsToolChoice(t *testing.T) {
	_, err := Complete(
		context.Background(),
		&recordingCompletionClient{},
		&model.Request{
			ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeNone},
		},
		testCompletionSpec(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not allow tool choice")
}

func TestStreamSetsStructuredOutputAndEnablesStreaming(t *testing.T) {
	spec := testCompletionSpec()
	client := &recordingCompletionClient{streamer: stubStreamer{}}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "create a task"}},
		}},
	}

	stream, err := Stream(context.Background(), client, req, spec)
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.NotNil(t, client.request)
	require.True(t, client.request.Stream)
	require.NotNil(t, client.request.StructuredOutput)
	assert.Equal(t, "draft_from_transcript", client.request.StructuredOutput.Name)
	assert.JSONEq(t, `{"type":"object","required":["assistant_text"]}`, string(client.request.StructuredOutput.Schema))
	assert.False(t, req.Stream)
	assert.Nil(t, req.StructuredOutput)
}

func TestStreamRejectsInvariantViolations(t *testing.T) {
	cases := []struct {
		name string
		req  *model.Request
		want string
	}{
		{
			name: "structured output override",
			req: &model.Request{
				StructuredOutput: &model.StructuredOutput{
					Name:   "other",
					Schema: tools.RawJSON(`{"type":"object"}`),
				},
			},
			want: "cannot override an existing structured output request",
		},
		{
			name: "tool definitions",
			req: &model.Request{
				Tools: []*model.ToolDefinition{{
					Name:        "lookup",
					Description: "Search",
					Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
				}},
			},
			want: "does not allow tool definitions",
		},
		{
			name: "tool choice",
			req: &model.Request{
				ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeNone},
			},
			want: "does not allow tool choice",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Stream(context.Background(), &recordingCompletionClient{}, tc.req, testCompletionSpec())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestStreamEnforcesCanonicalCompletionContract(t *testing.T) {
	spec := testCompletionSpec()
	upstream := &scriptedStreamer{
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: `{"assistant_text":"created a draft"}`},
				},
			}},
			StopReason: "stop",
		},
		results: []recvResult{
			{
				chunk: model.CompletionDeltaChunk{
					Delta: model.CompletionDelta{
						Name:  "draft_from_transcript",
						Delta: `{"assistant_text":"draft`,
					},
				},
			},
			{
				chunk: model.CompletionChunk{
					Completion: model.Completion{
						Name:    "draft_from_transcript",
						Payload: []byte(`{"assistant_text":"created a draft"}`),
					},
				},
			},
			{
				chunk: model.StopChunk{Reason: "stop"},
			},
			{err: io.EOF},
		},
	}
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{streamer: upstream},
		&model.Request{},
		spec,
	)
	require.NoError(t, err)

	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.CompletionDeltaChunk{}, chunk)

	chunk, err = stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.CompletionChunk{}, chunk)

	chunk, err = stream.Recv()
	require.NoError(t, err)
	require.IsType(t, model.StopChunk{}, chunk)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestStreamComparesCanonicalTypedCompletionValues(t *testing.T) {
	upstream := &scriptedStreamer{
		response: &model.Response{
			Content: []model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `{"summary":null,"assistant_text":"created a draft"}`}},
			}},
			StopReason: "stop",
		},
		results: []recvResult{
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
			{chunk: model.StopChunk{Reason: "stop"}},
			{err: io.EOF},
		},
	}
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{streamer: upstream},
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestStreamRejectsChunkAfterStop(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{streamer: &scriptedStreamer{results: []recvResult{
			{chunk: model.CompletionChunk{Completion: model.Completion{
				Name:    "draft_from_transcript",
				Payload: []byte(`{"assistant_text":"created a draft"}`),
			}}},
			{chunk: model.StopChunk{Reason: "stop"}},
			{chunk: model.UsageChunk{Usage: model.TokenUsage{TotalTokens: 1}}},
		}}},
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorContains(t, err, `emitted "usage" after stop`)
}

func TestStreamRejectsEOFBeforeFinalCompletion(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{
			streamer: &scriptedStreamer{results: []recvResult{{err: io.EOF}}},
		},
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, err, "ended without canonical completion chunk")
}

func TestStreamRejectsFinalCompletionWithoutMatchingCanonicalResponse(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{
			streamer: &scriptedStreamer{results: []recvResult{
				{chunk: model.CompletionChunk{Completion: model.Completion{
					Name:    "draft_from_transcript",
					Payload: []byte(`{"assistant_text":"created a draft"}`),
				}}},
				{chunk: model.StopChunk{Reason: "stop"}},
				{err: io.EOF},
			}},
		},
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorContains(t, err, "invalid canonical response")
}

func TestStreamRejectsStopBeforeFinalCompletion(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{
			streamer: &scriptedStreamer{
				results: []recvResult{{chunk: model.StopChunk{Reason: "stop"}}},
			},
		},
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, err, "stopped before canonical completion chunk")
}

func TestStreamRejectsUnexpectedTextChunk(t *testing.T) {
	stream, err := Stream(
		context.Background(),
		&recordingCompletionClient{
			streamer: &scriptedStreamer{
				results: []recvResult{{
					chunk: model.TextChunk{
						Message: model.Message{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"created a draft"}`}},
						},
					},
				}},
			},
		},
		&model.Request{},
		testCompletionSpec(),
	)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	require.ErrorContains(t, err, `unexpected "text" chunk`)
}

func TestStreamRejectsInvalidStructuredOutputChunks(t *testing.T) {
	cases := []struct {
		name    string
		results []recvResult
		advance int
		want    string
	}{
		{
			name: "missing completion delta fields",
			results: []recvResult{{
				chunk: model.CompletionDeltaChunk{},
			}},
			want: "completion delta is missing its name",
		},
		{
			name: "mismatched completion delta name",
			results: []recvResult{{
				chunk: model.CompletionDeltaChunk{
					Delta: model.CompletionDelta{
						Name:  "other",
						Delta: `{"assistant_text":"draft`,
					},
				},
			}},
			want: `completion delta for "other"`,
		},
		{
			name: "missing completion fields",
			results: []recvResult{{
				chunk: model.CompletionChunk{},
			}},
			want: "completion is missing its name",
		},
		{
			name: "mismatched completion name",
			results: []recvResult{{
				chunk: model.CompletionChunk{
					Completion: model.Completion{
						Name:    "other",
						Payload: []byte(`{"assistant_text":"created a draft"}`),
					},
				},
			}},
			want: `completion for "other"`,
		},
		{
			name: "duplicate canonical completion",
			results: []recvResult{
				{
					chunk: model.CompletionChunk{
						Completion: model.Completion{
							Name:    "draft_from_transcript",
							Payload: []byte(`{"assistant_text":"created a draft"}`),
						},
					},
				},
				{
					chunk: model.CompletionChunk{
						Completion: model.Completion{
							Name:    "draft_from_transcript",
							Payload: []byte(`{"assistant_text":"created a second draft"}`),
						},
					},
				},
			},
			advance: 1,
			want:    "multiple canonical completion chunks",
		},
		{
			name: "completion delta after final completion",
			results: []recvResult{
				{
					chunk: model.CompletionChunk{
						Completion: model.Completion{
							Name:    "draft_from_transcript",
							Payload: []byte(`{"assistant_text":"created a draft"}`),
						},
					},
				},
				{
					chunk: model.CompletionDeltaChunk{
						Delta: model.CompletionDelta{
							Name:  "draft_from_transcript",
							Delta: `{"assistant_text":"draft`,
						},
					},
				},
			},
			advance: 1,
			want:    "completion delta after final completion",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := Stream(
				context.Background(),
				&recordingCompletionClient{
					streamer: &scriptedStreamer{results: tc.results},
				},
				&model.Request{},
				testCompletionSpec(),
			)
			require.NoError(t, err)
			for i := 0; i < tc.advance; i++ {
				_, err = stream.Recv()
				require.NoError(t, err)
			}

			_, err = stream.Recv()
			require.Error(t, err)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDecodeResponseRejectsToolCalls(t *testing.T) {
	_, err := DecodeResponse(&model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "tool-1",
				Name:  "lookup",
				Input: rawjson.Message(`{}`),
			}},
		}},
		StopReason: "tool_use",
	}, testCompletionSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned tool calls")
}

func TestDecodeResponseRejectsMultipleAssistantMessages(t *testing.T) {
	_, err := DecodeResponse(&model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"first"}`}},
			},
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `{"assistant_text":"second"}`}},
			},
		},
		StopReason: "stop",
	}, testCompletionSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected exactly 1 assistant message")
}

func TestDecodeResponseRejectsMultipleContentParts(t *testing.T) {
	_, err := DecodeResponse(&model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: `{"assistant_text":"first"}`},
				model.TextPart{Text: `{"assistant_text":"second"}`},
			},
		}},
		StopReason: "stop",
	}, testCompletionSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple content parts")
}

func TestDecodeChunkIgnoresPreviewChunks(t *testing.T) {
	value, ok, err := DecodeChunk(model.CompletionDeltaChunk{
		Delta: model.CompletionDelta{
			Name:  "draft_from_transcript",
			Delta: `{"assistant_text":"draft`,
		},
	}, testCompletionSpec())
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, testCompletionResult{}, value)
}

func TestDecodeChunkDecodesFinalCompletion(t *testing.T) {
	value, ok, err := DecodeChunk(model.CompletionChunk{
		Completion: model.Completion{
			Name:    "draft_from_transcript",
			Payload: []byte(`{"assistant_text":"created a draft"}`),
		},
	}, testCompletionSpec())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, testCompletionResult{AssistantText: "created a draft"}, value)
}

func TestDecodeChunkRejectsMalformedCompletionChunk(t *testing.T) {
	_, ok, err := DecodeChunk(model.CompletionChunk{}, testCompletionSpec())
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "does not match spec")
}

func TestDecodeChunkRejectsWrongCompletionName(t *testing.T) {
	_, ok, err := DecodeChunk(model.CompletionChunk{
		Completion: model.Completion{
			Name:    "other",
			Payload: []byte(`{"assistant_text":"created a draft"}`),
		},
	}, testCompletionSpec())
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "does not match spec")
}
