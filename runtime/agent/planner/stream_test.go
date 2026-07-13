package planner

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	recordingEvents struct {
		usage []model.TokenUsage
	}

	testStreamer struct {
		chunks   []model.Chunk
		response *model.Response
		index    int
		closed   bool
	}
)

func (e *recordingEvents) AssistantChunk(context.Context, string) {}

func (e *recordingEvents) ToolCallArgsDelta(context.Context, string, tools.Ident, string) {}

func (e *recordingEvents) PlannerThinkingBlock(context.Context, model.ThinkingPart) {}

func (e *recordingEvents) PlannerThought(context.Context, string, map[string]string) {}

func (e *recordingEvents) UsageDelta(_ context.Context, usage model.TokenUsage) {
	e.usage = append(e.usage, usage)
}

func (s *testStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *testStreamer) Close() error {
	s.closed = true
	return nil
}

func (s *testStreamer) Response() *model.Response {
	return s.response
}

func TestConsumeStreamStampsUsageIdentityFromRequest(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{
			model.UsageChunk{
				Usage: model.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
			},
			model.StopChunk{Reason: "stop"},
		},
		response: &model.Response{
			Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}},
			StopReason: "stop",
			Usage:      model.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
		},
	}
	events := &recordingEvents{}

	summary, err := ConsumeStream(
		context.Background(),
		streamer,
		&model.Request{Model: "gpt-5", ModelClass: model.ModelClassHighReasoning},
		events,
	)

	require.NoError(t, err)
	require.True(t, streamer.closed)
	require.Equal(t, "gpt-5", summary.Usage.Model)
	require.Equal(t, model.ModelClassHighReasoning, summary.Usage.ModelClass)
	require.Equal(t, 2, summary.Usage.InputTokens)
	require.Equal(t, 3, summary.Usage.OutputTokens)
	require.Equal(t, 5, summary.Usage.TotalTokens)
	require.Len(t, events.usage, 1)
	require.Equal(t, "gpt-5", events.usage[0].Model)
	require.Equal(t, model.ModelClassHighReasoning, events.usage[0].ModelClass)
}

// TestConsumeStreamToolCallOmitsThoughtSignature documents that ConsumeStream
// deliberately does not surface model.ToolCall.ThoughtSignature on the
// resulting planner.ToolRequest: opaque provider state is captured earlier, at
// the runtime's model-client boundary (see runtime.modelInvocationClient),
// and never transits this user-facing type.
func TestConsumeStreamToolCallOmitsThoughtSignature(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{
				ToolCall: model.ToolCall{
					Name:             tools.Ident("svc.read.get_time_series"),
					ID:               "call-1",
					Payload:          []byte(`{}`),
					ThoughtSignature: "opaque-provider-signature",
				},
			},
			model.StopChunk{Reason: "tool_use"},
		},
		response: &model.Response{
			Content: []model.Message{{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.ToolUsePart{
					ID:    "call-1",
					Name:  "svc.read.get_time_series",
					Input: []byte(`{}`),
				}},
			}},
			StopReason: "tool_use",
		},
	}
	events := &recordingEvents{}

	summary, err := ConsumeStream(context.Background(), streamer, &model.Request{}, events)

	require.NoError(t, err)
	require.Len(t, summary.ToolCalls, 1)
	require.Equal(t, tools.Ident("svc.read.get_time_series"), summary.ToolCalls[0].Name)
	require.Equal(t, "call-1", summary.ToolCalls[0].ToolCallID)
}

func TestConsumeStreamRejectsRepeatedFinalizedToolCall(t *testing.T) {
	call := model.ToolCall{
		Name:    tools.Ident("svc.lookup"),
		ID:      "call-1",
		Payload: []byte(`{}`),
	}
	streamer := &testStreamer{
		chunks: []model.Chunk{
			model.ToolCallChunk{ToolCall: call},
			model.ToolCallChunk{ToolCall: call},
		},
	}

	_, err := ConsumeStream(context.Background(), streamer, &model.Request{}, &recordingEvents{})

	require.EqualError(t, err, `planner: model stream repeated finalized tool call "call-1"`)
	require.True(t, streamer.closed)
}

func TestConsumeStreamRejectsTypedCompletionChunks(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{model.CompletionChunk{
			Completion: model.Completion{
				Name:    "draft",
				Payload: []byte(`{"text":"done"}`),
			},
		}},
	}

	_, err := ConsumeStream(context.Background(), streamer, &model.Request{}, &recordingEvents{})

	require.EqualError(t, err, "planner: ConsumeStream does not accept typed completion chunks; use completion.Stream")
	require.True(t, streamer.closed)
}

func TestConsumeStreamUsesCanonicalResponseUsage(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{model.StopChunk{Reason: "stop"}},
		response: &model.Response{
			Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}},
			StopReason: "stop",
			Usage:      model.TokenUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		},
	}
	events := &recordingEvents{}

	summary, err := ConsumeStream(
		context.Background(),
		streamer,
		&model.Request{Model: "gpt-5", ModelClass: model.ModelClassDefault},
		events,
	)

	require.NoError(t, err)
	require.True(t, streamer.closed)
	require.Equal(t, "gpt-5", summary.Usage.Model)
	require.Equal(t, model.ModelClassDefault, summary.Usage.ModelClass)
	require.Equal(t, 1, summary.Usage.InputTokens)
	require.Equal(t, 2, summary.Usage.OutputTokens)
	require.Equal(t, 3, summary.Usage.TotalTokens)
	require.Len(t, events.usage, 1)
	require.Equal(t, "gpt-5", events.usage[0].Model)
	require.Equal(t, model.ModelClassDefault, events.usage[0].ModelClass)
}

func TestConsumeStreamRequiresCanonicalResponse(t *testing.T) {
	streamer := &testStreamer{}

	_, err := ConsumeStream(context.Background(), streamer, &model.Request{}, &recordingEvents{})

	require.ErrorContains(t, err, "planner: invalid canonical response")
	require.True(t, streamer.closed)
}

func TestStreamSummaryWithoutCanonicalResponseHasNoFinalResponse(t *testing.T) {
	require.Nil(t, (StreamSummary{Text: "presentation"}).FinalResponse())
}

func TestStreamSummaryFinalResponsePreservesCanonicalMessage(t *testing.T) {
	source := &model.Message{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "signature", Final: true},
			model.TextPart{Text: "canonical"},
		},
		Meta: map[string]any{"provider_item": "item-1"},
	}

	final := (StreamSummary{Text: "presentation", source: source}).FinalResponse()
	require.NotNil(t, final)
	require.Same(t, source, final.Message)
	require.Len(t, final.Message.Parts, 2)
	require.Equal(t, "item-1", final.Message.Meta["provider_item"])
}

func TestStreamSummaryWithToolCallsHasNoFinalResponse(t *testing.T) {
	require.Nil(t, (StreamSummary{
		source:    &model.Message{Role: model.ConversationRoleAssistant},
		ToolCalls: []ToolRequest{{Name: "svc.lookup"}},
	}).FinalResponse())
}
