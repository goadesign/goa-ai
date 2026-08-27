package planner

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

// mustPlannerToolInput compiles a static test schema.
func mustPlannerToolInput(schema []byte) model.ToolInput {
	input, err := model.AdvertisedToolInputFromSchema(schema)
	if err != nil {
		panic(err)
	}
	return input
}

type (
	testStreamer struct {
		chunks   []model.Chunk
		response *model.Response
		index    int
		closed   bool
		err      error
	}
)

func (s *testStreamer) Recv() (model.Chunk, error) {
	if s.index >= len(s.chunks) {
		if s.err != nil {
			return nil, s.err
		}
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

func TestConsumeStreamPreservesMissingProviderUsageModel(t *testing.T) {
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
	summary, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, &model.Request{
			Model:      "gpt-5",
			ModelClass: model.ModelClassHighReasoning,
		}),
	)

	require.NoError(t, err)
	require.True(t, streamer.closed)
	require.Empty(t, summary.Usage.Model)
	require.Equal(t, model.ModelClassHighReasoning, summary.Usage.ModelClass)
	require.Equal(t, 2, summary.Usage.InputTokens)
	require.Equal(t, 3, summary.Usage.OutputTokens)
	require.Equal(t, 5, summary.Usage.TotalTokens)
}

func TestConsumeStreamReportsOutputLimitedStop(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{model.StopChunk{
			Reason:        "max_tokens",
			OutputLimited: true,
		}},
		response: &model.Response{
			Content: []model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "partial"}},
			}},
			StopReason:    "max_tokens",
			OutputLimited: true,
		},
	}

	summary, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, &model.Request{}),
	)

	require.NoError(t, err)
	require.Equal(t, "max_tokens", summary.StopReason)
	require.True(t, summary.OutputLimited)
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
					ID:               "call-1",
					Name:             "svc.read.get_time_series",
					Input:            []byte(`{}`),
					ThoughtSignature: "opaque-provider-signature",
				}},
			}},
			StopReason: "tool_use",
		},
	}
	request := modelRequestWithTool("svc.read.get_time_series")
	summary, err := ConsumeStream(context.Background(), mustValidatedStream(t, streamer, request))

	require.NoError(t, err)
	require.Len(t, summary.ToolCalls, 1)
	require.Equal(t, tools.Ident("svc.read.get_time_series"), summary.ToolCalls[0].Name)
	require.Equal(t, "call-1", summary.ToolCalls[0].ModelToolCallID)
}

func TestConsumeStreamReturnsNoSummaryAfterLaterProviderRejection(t *testing.T) {
	request := modelRequestWithTool("svc.lookup")
	contract, err := model.NewRequestContract(request)
	require.NoError(t, err)
	streamer := &testStreamer{
		chunks: []model.Chunk{
			model.TextChunk{Message: model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "preview"}},
			}},
			model.ToolCallChunk{ToolCall: model.ToolCall{
				Name:    "svc.lookup",
				ID:      "call-1",
				Payload: []byte(`{}`),
			}},
		},
		err: contract.RejectProviderOutput(
			&model.TokenUsage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7},
			model.NewUnadvertisedToolNameError("svc.look_up"),
		),
	}

	summary, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, request),
	)

	require.Error(t, err)
	require.Equal(t, StreamSummary{}, summary)
	require.True(t, streamer.closed)
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

	request := modelRequestWithTool("svc.lookup")
	_, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, request),
	)

	var outputErr *OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, err, `model stream repeated finalized tool call "call-1"`)
	require.True(t, streamer.closed)
}

func TestConsumeStreamTextUsesValidatedAggregateBudget(t *testing.T) {
	text := strings.Repeat("x", (16<<20)/2)
	streamer := &testStreamer{chunks: []model.Chunk{
		model.TextChunk{Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: text}},
		}},
		model.TextChunk{Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: text}},
		}},
	}}

	summary, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, &model.Request{}),
	)

	require.ErrorContains(t, err, "exceeds maximum byte size")
	require.Equal(t, StreamSummary{}, summary)
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

	_, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, &model.Request{}),
	)

	var outputErr *OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, err, "model stream emitted a completion without a structured output request")
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
	summary, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, &model.Request{
			Model:      "gpt-5",
			ModelClass: model.ModelClassDefault,
		}),
	)

	require.NoError(t, err)
	require.True(t, streamer.closed)
	require.Empty(t, summary.Usage.Model)
	require.Equal(t, model.ModelClassDefault, summary.Usage.ModelClass)
	require.Equal(t, 1, summary.Usage.InputTokens)
	require.Equal(t, 2, summary.Usage.OutputTokens)
	require.Equal(t, 3, summary.Usage.TotalTokens)
}

func TestConsumeStreamRequiresCanonicalResponse(t *testing.T) {
	streamer := &testStreamer{
		chunks: []model.Chunk{model.StopChunk{Reason: "stop"}},
	}

	_, err := ConsumeStream(
		context.Background(),
		mustValidatedStream(t, streamer, &model.Request{}),
	)

	require.ErrorContains(t, err, "invalid canonical response")
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

func modelRequestWithTool(name string) *model.Request {
	return &model.Request{Tools: []*model.ToolDefinition{{
		Name:  name,
		Input: mustPlannerToolInput([]byte(`{"type":"object"}`)),
	}}}
}

func mustValidatedStream(t *testing.T, streamer model.Streamer, request *model.Request) *model.ValidatedStream {
	t.Helper()
	contract, err := model.NewRequestContract(request)
	require.NoError(t, err)
	validated, err := contract.ValidateStream(streamer)
	require.NoError(t, err)
	return validated
}
