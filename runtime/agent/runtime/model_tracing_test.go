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
		meta     map[string]any
		response *model.Response
		index    int
		recvErr  error
		closeErr error
	}
)

func testGenAIContext() telemetry.GenAIContext {
	return telemetry.GenAIContext{
		ConversationID: "sess-1",
		AgentID:        "svc.agent",
		AgentName:      "svc.agent",
	}
}

func TestTracedClientStreamIgnoresCanceledStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(stubModelClient{
		stream: func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, context.Canceled
		},
	}, tracer, telemetry.NewNoopLogger(), "bedrock", testGenAIContext(), false)

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
	client := newTracedClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return nil, grpcStatus.Error(grpcCodes.Canceled, "context canceled")
		},
	}, tracer, telemetry.NewNoopLogger(), "bedrock", testGenAIContext(), false)

	resp, err := client.Complete(ctx, &model.Request{ModelClass: model.ModelClassDefault})
	require.Equal(t, grpcCodes.Canceled, grpcStatus.Code(err))
	assert.Nil(t, resp)
	require.Len(t, tracer.spans, 1)
	assert.Empty(t, tracer.spans[0].errs)
	assert.Equal(t, codes.Unset, tracer.spans[0].statusCode)
	assert.True(t, tracer.spans[0].ended)
}

func TestTracedStreamRecvIgnoresContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:   ctx,
		inner: &stubStreamer{recvErr: context.Canceled},
		span:  span,
	}

	_, err := stream.Recv()
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, span.errs)
	assert.Equal(t, codes.Unset, span.statusCode)
	assert.True(t, span.ended)
}

func TestTracedStreamRecvRecordsNonCancellationError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:   context.Background(),
		inner: &stubStreamer{recvErr: wantErr},
		span:  span,
	}

	_, err := stream.Recv()
	require.ErrorIs(t, err, wantErr)
	require.Len(t, span.errs, 1)
	require.ErrorIs(t, span.errs[0], wantErr)
	assert.Equal(t, codes.Error, span.statusCode)
	assert.Equal(t, "stream recv failed", span.statusDesc)
	assert.True(t, span.ended)
}

func TestTracedClientCompleteEmitsGenAIAttrs(t *testing.T) {
	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
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
	}, tracer, telemetry.NewNoopLogger(), "primary", telemetry.GenAIContext{
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

func TestTracedStreamUsesMetadataUsageWhenNoUsageDelta(t *testing.T) {
	t.Parallel()

	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:  context.Background(),
		span: span,
		inner: &stubStreamer{
			meta: map[string]any{
				"usage": model.TokenUsage{
					Model:        "us.anthropic.claude-sonnet-4",
					InputTokens:  7,
					OutputTokens: 3,
				},
			},
		},
	}

	_, err := stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	attrs := attrsByKey(span.attrs)
	assert.Equal(t, "us.anthropic.claude-sonnet-4", attrs[telemetry.AttrGenAIResponseModel].AsString())
	assert.EqualValues(t, 7, attrs[telemetry.AttrGenAIUsageInputTokens].AsInt64())
	assert.EqualValues(t, 3, attrs[telemetry.AttrGenAIUsageOutputTokens].AsInt64())
}

func TestTracedStreamDoesNotDoubleCountMetadataAfterUsageDelta(t *testing.T) {
	t.Parallel()

	span := &recordingTelemetrySpan{}
	stream := &tracedStream{
		ctx:  context.Background(),
		span: span,
		inner: &stubStreamer{
			chunks: []model.Chunk{
				model.UsageChunk{
					Usage: model.TokenUsage{
						Model:        "delta-model",
						InputTokens:  2,
						OutputTokens: 4,
					},
				},
			},
			meta: map[string]any{
				"usage": model.TokenUsage{
					Model:        "metadata-model",
					InputTokens:  99,
					OutputTokens: 99,
				},
			},
		},
	}

	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	attrs := attrsByKey(span.attrs)
	assert.Equal(t, "delta-model", attrs[telemetry.AttrGenAIResponseModel].AsString())
	assert.EqualValues(t, 2, attrs[telemetry.AttrGenAIUsageInputTokens].AsInt64())
	assert.EqualValues(t, 4, attrs[telemetry.AttrGenAIUsageOutputTokens].AsInt64())
}

func TestTracedClientCompleteRecordsGenAIMessagesWhenEnabled(t *testing.T) {
	t.Parallel()

	tracer := &recordingTelemetryTracer{}
	client := newTracedClient(stubModelClient{
		complete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.TextPart{Text: "I will check."},
						model.ToolUsePart{
							ID:    "call-1",
							Name:  "atlas.read",
							Input: rawjson.Message(`{"asset":"pump"}`),
						},
					},
				}},
				StopReason: "tool_use",
			}, nil
		},
	}, tracer, telemetry.NewNoopLogger(), "primary", testGenAIContext(), true)

	_, err := client.Complete(context.Background(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "diagnose pump"}},
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
					"content": "diagnose pump"
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
					"name": "atlas.read",
					"arguments": {
						"asset": "pump"
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
	client := newTracedClient(stubModelClient{
		complete: func(_ context.Context, _ *model.Request) (*model.Response, error) {
			return &model.Response{
				Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "hi"}}}},
				StopReason: "end_turn",
			}, nil
		},
	}, tracer, telemetry.NewNoopLogger(), "primary", testGenAIContext(), false)

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
	client := newTracedClient(stubModelClient{
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
			}}, nil
		},
	}, tracer, telemetry.NewNoopLogger(), "primary", testGenAIContext(), true)

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
	return c.stream(ctx, req)
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

func (s *stubStreamer) Metadata() map[string]any {
	return s.meta
}
