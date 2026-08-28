package runtime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

type (
	recordingTelemetryTracer struct {
		spans []*recordingTelemetrySpan
	}

	recordingTelemetrySpan struct {
		name       string
		attrs      []attribute.KeyValue
		statusCode codes.Code
		statusDesc string
		errs       []error
		ended      bool
	}

	stubModelClient struct {
		complete func(context.Context, *model.Request) (*model.Response, error)
		stream   func(context.Context, *model.Request) (model.Streamer, error)
	}

	stubStreamer struct {
		chunks   []model.Chunk
		response *model.Response
		index    int
		recvErr  error
		closeErr error
	}

	failingCallPreparationProvider struct {
		err      error
		observer model.ClientCallObserver
		calls    int
	}
)

func testGenAIContext() telemetry.GenAIContext {
	return telemetry.GenAIContext{
		ConversationID: "sess-1",
		AgentID:        "svc.agent",
		AgentName:      "svc.agent",
	}
}

func TestTracedClientClosesStreamReturnedWithError(t *testing.T) {
	callErr := errors.New("stream call failed")
	closeErr := errors.New("stream close failed")
	request := &model.Request{ModelClass: model.ModelClassDefault}
	raw := &chunkStreamer{closeErr: closeErr}
	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(
		mustTestModelClient(streamResultClient{stream: raw, err: callErr}),
		tracer,
		telemetry.NewNoopLogger(),
		"bedrock",
		testGenAIContext(),
		false,
	)

	got, err := client.Stream(t.Context(), request)

	require.Nil(t, got)
	require.ErrorIs(t, err, callErr)
	require.ErrorIs(t, err, closeErr)
	require.True(t, raw.closed)
	require.Len(t, tracer.spans, 1)
	require.True(t, tracer.spans[0].ended)
}

func TestPreparedRuntimeObserversAbortWhenInnerPreparationFails(t *testing.T) {
	setupErr := errors.New("inner observer setup failed")
	raw := &failingCallPreparationProvider{err: setupErr}
	sink := &fakeModelInvocationSink{}
	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(
		newModelInvocationClient(mustTestModelClient(raw), sink),
		tracer,
		telemetry.NewNoopLogger(),
		"bedrock",
		testGenAIContext(),
		false,
	)

	response, err := client.Complete(t.Context(), &model.Request{
		ModelClass: model.ModelClassDefault,
	})

	require.Nil(t, response)
	require.ErrorIs(t, err, setupErr)
	require.Zero(t, raw.calls)
	require.Len(t, tracer.spans, 1)
	require.True(t, tracer.spans[0].ended)
	require.Contains(t, sink.finished, sink.last)
	require.ErrorIs(t, sink.finished[sink.last], setupErr)
}

func TestTracedClientRecordsConfiguredCompletionFailure(t *testing.T) {
	tracer := &recordingTelemetryTracer{}
	invocations := &modelInvocationJournal{}
	checked := newModelInvocationClient(mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: `"invalid"`}},
			}}), nil
		},
	}), invocations)
	client := newTracedClient(
		checked,
		tracer,
		telemetry.NewNoopLogger(),
		"bedrock",
		testGenAIContext(),
		false,
	)
	request := &model.Request{
		ModelClass: model.ModelClassDefault,
		StructuredOutput: &model.StructuredOutput{
			Name:   "answer",
			Schema: []byte(`{"type":"string"}`),
		},
	}
	require.NoError(t, model.SetCompletionValidator(
		request,
		func(*model.Response, *model.Completion) error {
			return errors.New("typed completion is invalid")
		},
	))

	response, err := client.Complete(t.Context(), request)

	require.Nil(t, response)
	require.ErrorContains(t, err, "typed completion is invalid")
	require.Len(t, tracer.spans, 1)
	require.Len(t, tracer.spans[0].errs, 1)
	require.Equal(t, codes.Error, tracer.spans[0].statusCode)
}

func TestTracedStreamRecordsConfiguredCompletionFailure(t *testing.T) {
	tracer := &recordingTelemetryTracer{}
	checked := newModelInvocationClient(mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return &stubStreamer{
				chunks: []model.Chunk{model.TextChunk{Message: model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "not structured"}},
				}}},
			}, nil
		},
	}), &modelInvocationJournal{})
	client := newTracedClient(
		checked,
		tracer,
		telemetry.NewNoopLogger(),
		"bedrock",
		testGenAIContext(),
		false,
	)
	request := &model.Request{
		ModelClass: model.ModelClassDefault,
		StructuredOutput: &model.StructuredOutput{
			Name:   "answer",
			Schema: []byte(`{"type":"object"}`),
		},
	}
	require.NoError(t, model.SetCompletionValidator(
		request,
		func(*model.Response, *model.Completion) error {
			return nil
		},
	))
	stream, err := client.Stream(t.Context(), request)
	require.NoError(t, err)

	_, err = stream.Recv()

	require.ErrorContains(t, err, "text instead of a completion")
	require.Len(t, tracer.spans, 1)
	require.Len(t, tracer.spans[0].errs, 1)
	require.Equal(t, codes.Error, tracer.spans[0].statusCode)
}

func TestTracedClientStreamIgnoresCanceledStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, context.Canceled
		},
	}), tracer, telemetry.NewNoopLogger(), "bedrock", testGenAIContext(), false)

	stream, err := client.Stream(ctx, &model.Request{
		ModelClass: model.ModelClassDefault,
		Stream:     true,
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, stream)
	require.Len(t, tracer.spans, 1)
	assert.Empty(t, tracer.spans[0].errs)
	assert.Equal(t, codes.Unset, tracer.spans[0].statusCode)
	assert.True(t, tracer.spans[0].ended)
}

func TestTracedClientCompleteIgnoresContextTermination(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return nil, grpcStatus.Error(grpcCodes.Canceled, "context canceled")
		},
	}), tracer, telemetry.NewNoopLogger(), "bedrock", testGenAIContext(), false)

	resp, err := client.Complete(ctx, &model.Request{ModelClass: model.ModelClassDefault})
	require.Equal(t, grpcCodes.Canceled, grpcStatus.Code(err))
	assert.Nil(t, resp)
	require.Len(t, tracer.spans, 1)
	assert.Empty(t, tracer.spans[0].errs)
	assert.Equal(t, codes.Unset, tracer.spans[0].statusCode)
	assert.True(t, tracer.spans[0].ended)
}

func TestTracedClientDropsResponseReturnedWithError(t *testing.T) {
	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse([]model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "failed"}},
			}}), assert.AnError
		},
	}), tracer, telemetry.NewNoopLogger(), "bedrock", testGenAIContext(), false)

	response, err := client.Complete(context.Background(), &model.Request{
		ModelClass: model.ModelClassDefault,
	})

	require.Nil(t, response)
	require.ErrorIs(t, err, assert.AnError)
}

func TestTracedStreamObserverIgnoresContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:  ctx,
		span: span,
	}

	err := stream.ObserveStreamRecv(model.StreamObservation{Err: context.Canceled})
	require.NoError(t, err)
	assert.Empty(t, span.errs)
	assert.Equal(t, codes.Unset, span.statusCode)
	assert.False(t, span.ended)
}

func TestTracedStreamObserverRecordsNonCancellationError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:  context.Background(),
		span: span,
	}

	err := stream.ObserveStreamRecv(model.StreamObservation{Err: wantErr})
	require.NoError(t, err)
	require.Len(t, span.errs, 1)
	require.ErrorIs(t, span.errs[0], wantErr)
	assert.Equal(t, codes.Error, span.statusCode)
	assert.Equal(t, "stream recv failed", span.statusDesc)
	assert.False(t, span.ended)
}

func TestTracedClientCompleteEmitsGenAIAttrs(t *testing.T) {
	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				}},
				Usage: model.TokenUsage{
					Model:            "us.anthropic.claude-sonnet-4",
					InputTokens:      12,
					OutputTokens:     5,
					CacheReadTokens:  3,
					CacheWriteTokens: 2,
				},
				StopReason: "stop",
			}, nil
		},
	}), tracer, telemetry.NewNoopLogger(), "primary", telemetry.GenAIContext{
		ConversationID: "sess-1",
		AgentID:        "svc.agent",
		AgentName:      "svc.agent",
	}, false)

	resp, err := client.Complete(context.Background(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		MaxTokens:  512,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	assert.Equal(t, "chat high-reasoning", span.name)
	attrs := attrsByKey(span.attrs)
	assert.Equal(t, telemetry.GenAIOperationChat, attrs[telemetry.AttrGenAIOperationName].AsString())
	assert.Equal(t, "sess-1", attrs[telemetry.AttrGenAIConversationID].AsString())
	assert.Equal(t, "svc.agent", attrs[telemetry.AttrGenAIAgentName].AsString())
	assert.Equal(t, "high-reasoning", attrs[telemetry.AttrGenAIRequestModel].AsString())
	assert.EqualValues(t, 512, attrs[telemetry.AttrGenAIRequestMaxTokens].AsInt64())
	assert.Equal(t, "us.anthropic.claude-sonnet-4", attrs[telemetry.AttrGenAIResponseModel].AsString())
	assert.EqualValues(t, 12, attrs[telemetry.AttrGenAIUsageInputTokens].AsInt64())
	assert.EqualValues(t, 5, attrs[telemetry.AttrGenAIUsageOutputTokens].AsInt64())
	assert.EqualValues(t, 3, attrs[telemetry.AttrGenAIUsageCacheReadTokens].AsInt64())
	assert.EqualValues(t, 2, attrs[telemetry.AttrGenAIUsageCacheCreationToken].AsInt64())
	assert.Equal(t, []string{"stop"}, attrs[telemetry.AttrGenAIResponseFinishReasons].AsStringSlice())
}

func TestTracedStreamUsesResponseUsageWhenNoUsageChunk(t *testing.T) {
	t.Parallel()

	span := &recordingTelemetrySpan{}
	response := &model.Response{
		Usage: model.TokenUsage{
			Model:        "us.anthropic.claude-sonnet-4",
			InputTokens:  7,
			OutputTokens: 3,
		},
	}
	stream := &tracedStream{
		ctx:  context.Background(),
		span: span,
	}

	err := stream.ObserveStreamRecv(model.StreamObservation{Response: response, Err: io.EOF})
	require.NoError(t, err)

	attrs := attrsByKey(span.attrs)
	assert.Equal(t, "us.anthropic.claude-sonnet-4", attrs[telemetry.AttrGenAIResponseModel].AsString())
	assert.EqualValues(t, 7, attrs[telemetry.AttrGenAIUsageInputTokens].AsInt64())
	assert.EqualValues(t, 3, attrs[telemetry.AttrGenAIUsageOutputTokens].AsInt64())
}

func TestTracedStreamDoesNotReplaceUsageChunkWithResponseUsage(t *testing.T) {
	t.Parallel()

	span := &recordingTelemetrySpan{}
	response := &model.Response{
		Usage: model.TokenUsage{
			Model:        "response-model",
			InputTokens:  99,
			OutputTokens: 99,
		},
	}
	stream := &tracedStream{
		ctx:  context.Background(),
		span: span,
	}

	err := stream.ObserveStreamRecv(model.StreamObservation{
		Chunk: model.UsageChunk{
			Usage: model.TokenUsage{
				Model:        "delta-model",
				InputTokens:  2,
				OutputTokens: 4,
			},
		},
	})
	require.NoError(t, err)
	err = stream.ObserveStreamRecv(model.StreamObservation{Response: response, Err: io.EOF})
	require.NoError(t, err)

	attrs := attrsByKey(span.attrs)
	assert.Equal(t, "delta-model", attrs[telemetry.AttrGenAIResponseModel].AsString())
	assert.EqualValues(t, 2, attrs[telemetry.AttrGenAIUsageInputTokens].AsInt64())
	assert.EqualValues(t, 4, attrs[telemetry.AttrGenAIUsageOutputTokens].AsInt64())
}

func TestTracedClientCompleteRecordsGenAIMessagesWhenEnabled(t *testing.T) {
	t.Parallel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		complete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.TextPart{Text: "I will check."},
						model.ToolUsePart{
							ID:    "call-1",
							Name:  "catalog.lookup",
							Input: rawjson.Message(`{"record":"record_1"}`),
						},
					},
				}},
				StopReason: "tool_use",
			}, nil
		},
	}), tracer, telemetry.NewNoopLogger(), "primary", testGenAIContext(), true)

	_, err := client.Complete(context.Background(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "review record"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:  "catalog.lookup",
			Input: mustRuntimeToolInput(rawjson.Message(`{"type":"object"}`)),
		}},
	})
	require.NoError(t, err)

	require.Len(t, tracer.spans, 1)
	attrs := attrsByKey(tracer.spans[0].attrs)
	require.JSONEq(t, `[
		{
			"role": "user",
			"parts": [
				{
					"type": "text",
					"content": "review record"
				}
			]
		}
	]`, attrs[telemetry.AttrGenAIInputMessages].AsString())
	require.JSONEq(t, `[
		{
			"role": "assistant",
			"parts": [
				{
					"type": "text",
					"content": "I will check."
				},
				{
					"type": "tool_call",
					"id": "call-1",
					"name": "catalog.lookup",
					"arguments": {
						"record": "record_1"
					}
				}
			],
			"finish_reason": "tool_use"
		}
	]`, attrs[telemetry.AttrGenAIOutputMessages].AsString())
}

func TestTracedClientCompleteSkipsMessagesWhenCaptureDisabled(t *testing.T) {
	t.Parallel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		complete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return &model.Response{
				Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
				StopReason: "end_turn",
			}, nil
		},
	}), tracer, telemetry.NewNoopLogger(), "primary", testGenAIContext(), false)

	_, err := client.Complete(context.Background(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages:   []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
	})
	require.NoError(t, err)

	require.Len(t, tracer.spans, 1)
	attrs := attrsByKey(tracer.spans[0].attrs)
	_, hasInput := attrs[telemetry.AttrGenAIInputMessages]
	_, hasOutput := attrs[telemetry.AttrGenAIOutputMessages]
	assert.False(t, hasInput)
	assert.False(t, hasOutput)
}

func TestTracedStreamRecordsBufferedOutputMessagesWhenEnabled(t *testing.T) {
	t.Parallel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(mustTestModelClient(stubModelClient{
		stream: func(_ context.Context, _ *model.Request) (model.Streamer, error) {
			return &stubStreamer{chunks: []model.Chunk{
				model.TextChunk{
					Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hel"}}},
				},
				model.TextChunk{
					Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "lo"}}},
				},
				model.ThinkingChunk{
					Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "draft", Final: false}}},
				},
				model.ThinkingChunk{
					Message: model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "draft", Final: true}}},
				},
				model.StopChunk{Reason: "end_turn"},
			}, response: &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.TextPart{Text: "hello"},
						model.ThinkingPart{Text: "draft", Final: true},
					},
				}},
				StopReason: "end_turn",
			}}, nil
		},
	}), tracer, telemetry.NewNoopLogger(), "primary", testGenAIContext(), true)

	stream, err := client.Stream(context.Background(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages:   []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
	})
	require.NoError(t, err)
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	require.Len(t, tracer.spans, 1)
	attrs := attrsByKey(tracer.spans[0].attrs)
	require.JSONEq(t, `[
		{
			"role": "user",
			"parts": [
				{
					"type": "text",
					"content": "hi"
				}
			]
		}
	]`, attrs[telemetry.AttrGenAIInputMessages].AsString())
	require.JSONEq(t, `[
		{
			"role": "assistant",
			"parts": [
				{
					"type": "text",
					"content": "hello"
				}
			],
			"finish_reason": "end_turn"
		}
	]`, attrs[telemetry.AttrGenAIOutputMessages].AsString())
	require.NotContains(t, attrs[telemetry.AttrGenAIOutputMessages].AsString(), "draft")
}

func (t *recordingTelemetryTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	span := &recordingTelemetrySpan{
		name:  name,
		attrs: cfg.Attributes(),
	}
	t.spans = append(t.spans, span)
	return ctx, span
}

func (t *recordingTelemetryTracer) Span(context.Context) telemetry.Span {
	if len(t.spans) == 0 {
		return &recordingTelemetrySpan{}
	}
	return t.spans[len(t.spans)-1]
}

func (s *recordingTelemetrySpan) End(...trace.SpanEndOption) {
	s.ended = true
}

func (s *recordingTelemetrySpan) AddEvent(string, ...any) {}

func (s *recordingTelemetrySpan) SetAttributes(attrs ...attribute.KeyValue) {
	s.attrs = append(s.attrs, attrs...)
}

func (s *recordingTelemetrySpan) SetStatus(code codes.Code, description string) {
	s.statusCode = code
	s.statusDesc = description
}

func (s *recordingTelemetrySpan) RecordError(err error, _ ...trace.EventOption) {
	if err != nil {
		s.errs = append(s.errs, err)
	}
}

func attrsByKey(attrs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	out := make(map[attribute.Key]attribute.Value, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value
	}
	return out
}

func (c stubModelClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if c.complete == nil {
		return nil, errors.New("unexpected Complete call")
	}
	return c.complete(ctx, req)
}

func (c stubModelClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	if c.stream == nil {
		return nil, errors.New("unexpected Stream call")
	}
	return c.stream(ctx, req)
}

func (p *failingCallPreparationProvider) PrepareClientCall(
	ctx context.Context,
	_ *model.Request,
) (context.Context, model.ClientCallObserver, error) {
	return ctx, p.observer, p.err
}

func (p *failingCallPreparationProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	p.calls++
	return nil, errors.New("unexpected Complete call")
}

func (p *failingCallPreparationProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	p.calls++
	return nil, errors.New("unexpected Stream call")
}

func mustTestModelClient(provider model.Provider) model.Client {
	client, err := model.NewClient(provider)
	if err != nil {
		panic(err)
	}
	return client
}

func (s *stubStreamer) Recv() (model.Chunk, error) {
	if s.index < len(s.chunks) {
		chunk := s.chunks[s.index]
		s.index++
		return chunk, nil
	}
	if s.recvErr != nil {
		return nil, s.recvErr
	}
	return nil, io.EOF
}

func (s *stubStreamer) Close() error {
	return s.closeErr
}

func (s *stubStreamer) Response() *model.Response {
	return s.response
}
