package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/runtime/agent/model"
)

// recordingConverseRuntime returns a fixed Converse response so tests can
// exercise Client.Complete's response-decoding path without a live Bedrock
// endpoint.
type recordingConverseRuntime struct {
	output *bedrockruntime.ConverseOutput
}

func (r *recordingConverseRuntime) Converse(
	_ context.Context,
	_ *bedrockruntime.ConverseInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	return r.output, nil
}

func (r *recordingConverseRuntime) ConverseStream(
	_ context.Context,
	_ *bedrockruntime.ConverseStreamInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseStreamOutput, error) {
	return nil, nil
}

func (r *recordingConverseRuntime) CountTokens(
	_ context.Context,
	_ *bedrockruntime.CountTokensInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.CountTokensOutput, error) {
	return nil, nil
}

func strPtr(s string) *string { return &s }

// smithyDocumentFromJSON builds the document.Interface Bedrock's SDK returns
// for a ToolUseBlock.Input, so tests can simulate a decoded tool_use payload.
func smithyDocumentFromJSON(t *testing.T, raw string) document.Interface {
	var v any
	require.NoError(t, json.Unmarshal([]byte(raw), &v))
	return document.NewLazyDocument(v)
}

// These tests prove that Bedrock uses OutputConfig for Claude models that
// support it and one private tool for Claude models that do not. Both paths
// return the same provider-neutral structured completion.

func TestPrepareRequestAnthropicStructuredOutputUsesToolFallback(t *testing.T) {
	client := &provider{defaultModel: "us.anthropic.claude-opus-5"}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "draft the task"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:                     "complete_draft",
			Description:              "Return the completed task draft.",
			Schema:                   []byte(`{"type":"object","example":{"title":"Inspect evaporator"},"required":["title"],"properties":{"title":{"type":"string"}}}`),
			SchemaWithoutRootExample: []byte(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`),
			ExampleJSON:              []byte(`{"title":"Inspect evaporator"}`),
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	require.Nil(t, parts.outputConfig, "must not use native OutputConfig for Anthropic models")
	require.NotNil(t, parts.toolConfig, "must force a single tool call instead")
	require.Len(t, parts.toolConfig.Tools, 1)

	choice, ok := parts.toolConfig.ToolChoice.(*brtypes.ToolChoiceMemberTool)
	require.True(t, ok, "expected ToolChoiceMemberTool")
	require.Equal(t, "complete_draft", parts.toolNameProvToCanonical[*choice.Value.Name])

	spec, ok := parts.toolConfig.Tools[0].(*brtypes.ToolMemberToolSpec)
	require.True(t, ok)
	require.Equal(t, "Return the completed task draft.", *spec.Value.Description)
	require.NotNil(t, spec.Value.Strict)
	require.True(t, *spec.Value.Strict)
	require.Equal(t, []string{"tool-examples-2025-10-29"}, parts.additionalModelFields["anthropic_beta"])
	tools, ok := parts.additionalModelFields["tools"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	require.Equal(t, []map[string]any{{
		"value": map[string]any{"title": "Inspect evaporator"},
	}}, tools[0]["input_examples"])
	require.Equal(t, map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"value"},
		"properties": map[string]any{
			"value": map[string]any{
				"type":       "object",
				"required":   []any{"title"},
				"properties": map[string]any{"title": map[string]any{"type": "string"}},
			},
		},
	}, tools[0]["input_schema"])
}

func TestPrepareRequestNovaStructuredOutputUsesNativeOutputConfig(t *testing.T) {
	client := &provider{defaultModel: "amazon.nova-pro-v1:0"}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "draft the task"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:        "complete_draft",
			Description: "Return the completed task draft.",
			Schema:      []byte(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`),
		},
	}

	parts, err := client.prepareRequest(req)
	require.NoError(t, err)
	require.NotNil(t, parts.outputConfig, "Nova must keep using native OutputConfig")
	require.Nil(t, parts.toolConfig)
}

func TestPrepareRequestClaude45StructuredOutputUsesNativeOutputConfig(t *testing.T) {
	client := &provider{defaultModel: "us.anthropic.claude-sonnet-4-5-20250929-v1:0"}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "draft the task"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:        "complete_draft",
			Description: "Return the completed task draft.",
			Schema:      []byte(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`),
		},
	}

	parts, err := client.prepareRequest(req)

	require.NoError(t, err)
	require.NotNil(t, parts.outputConfig)
	require.Nil(t, parts.toolConfig)
}

func TestPrepareRequestStructuredOutputRejectsExplicitTools(t *testing.T) {
	client := &provider{defaultModel: "us.anthropic.claude-opus-5"}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "draft the task"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:        "complete_draft",
			Description: "Return the completed task draft.",
			Schema:      []byte(`{"type":"object"}`),
		},
		ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeAny},
	}

	_, err := client.prepareRequest(req)
	require.ErrorContains(t, err, "structured output cannot be combined with request tool definitions")
}

func TestAnthropicStructuredOutputRejectsLegacyThinkingBeforeProviderCall(t *testing.T) {
	client := &provider{
		defaultModel: "us.anthropic.claude-sonnet-4-0",
		runtime:      &recordingConverseRuntime{},
	}
	newRequest := func() *model.Request {
		req := &model.Request{
			Messages: []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "draft the task"}},
			}},
			Thinking: &model.ThinkingOptions{
				Enable:       true,
				BudgetTokens: 2048,
			},
			StructuredOutput: &model.StructuredOutput{
				Name:        "complete_draft",
				Description: "Return the completed task draft.",
				Schema:      []byte(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`),
			},
		}
		require.NoError(t, model.SetCompletionValidator(
			req,
			func(*model.Response, *model.Completion) error { return nil },
		))
		return req
	}

	t.Run("unary", func(t *testing.T) {
		_, err := client.Complete(t.Context(), newRequest())
		require.EqualError(t, err, "bedrock: manual thinking cannot be combined with forced tool choice")
	})
	t.Run("streaming", func(t *testing.T) {
		_, err := client.Stream(t.Context(), newRequest())
		require.EqualError(t, err, "bedrock: manual thinking cannot be combined with forced tool choice")
	})
}

// TestCompleteAnthropicStructuredOutputReifiesToolCall proves the adapter turns
// Bedrock's forced tool response into the completion text required by the
// provider-neutral contract while preserving provider-issued thinking.
func TestCompleteAnthropicStructuredOutputReifiesToolCall(t *testing.T) {
	runtime := &recordingConverseRuntime{
		output: &bedrockruntime.ConverseOutput{
			StopReason: brtypes.StopReasonToolUse,
			Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberReasoningContent{
						Value: &brtypes.ReasoningContentBlockMemberReasoningText{
							Value: brtypes.ReasoningTextBlock{
								Text:      aws.String("reasoning"),
								Signature: aws.String("signature"),
							},
						},
					},
					&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
						ToolUseId: strPtr("tooluse_1"),
						Name:      strPtr("complete_draft"),
						Input:     smithyDocumentFromJSON(t, `{"value":{"title":"Inspect evaporator"}}`),
					}},
				},
			}},
		},
	}
	client := &provider{defaultModel: "us.anthropic.claude-opus-5", runtime: runtime}
	req := &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "draft the task"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:        "complete_draft",
			Description: "Return the completed task draft.",
			Schema:      []byte(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`),
		},
	}
	require.NoError(t, model.SetCompletionValidator(
		req,
		func(*model.Response, *model.Completion) error { return nil },
	))

	resp, err := client.Complete(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	require.Len(t, resp.Content[0].Parts, 2)
	require.Equal(t, model.ThinkingPart{Text: "reasoning", Signature: "signature", Final: true}, resp.Content[0].Parts[0])
	text, ok := resp.Content[0].Parts[1].(model.TextPart)
	require.True(t, ok, "forced tool call must be reified into a TextPart")
	require.JSONEq(t, `{"title":"Inspect evaporator"}`, text.Text)
	require.Empty(t, resp.ToolCalls(), "the canonical response must not surface tool calls")
}

// TestChunkProcessorStructuredOutputToolFallbackEmitsCompletion proves the
// streaming decoder removes the private tool envelope and emits the same final
// completion payload as native OutputConfig streaming. It suppresses preview
// deltas because the synthetic tool fragments contain the private envelope.
func TestChunkProcessorStructuredOutputToolFallbackEmitsCompletion(t *testing.T) {
	idx := int32(0)
	var chunks []model.Chunk

	cp := newChunkProcessor(
		func(ch model.Chunk) error {
			chunks = append(chunks, ch)
			return nil
		},
		map[string]string{"complete_draft": "complete_draft"},
		"test-model-id",
		model.ModelClassDefault,
		&model.StructuredOutput{
			Name:   "complete_draft",
			Schema: []byte(`{"type":"object"}`),
		},
		"complete_draft",
	)

	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStart{}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &idx,
			Start: &brtypes.ContentBlockStartMemberToolUse{
				Value: brtypes.ToolUseBlockStart{
					ToolUseId: strPtr("tooluse_1"),
					Name:      strPtr("complete_draft"),
				},
			},
		},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &idx,
			Delta: &brtypes.ContentBlockDeltaMemberToolUse{
				Value: brtypes.ToolUseBlockDelta{Input: strPtr(`{"value":{"title":"Inspect evaporator"}}`)},
			},
		},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &idx},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonToolUse},
	}))
	require.NoError(t, cp.Handle(&brtypes.ConverseStreamOutputMemberMetadata{
		Value: brtypes.ConverseStreamMetadataEvent{},
	}))

	require.Len(t, chunks, 2)
	completion, ok := chunks[0].(model.CompletionChunk)
	require.True(t, ok, "expected a canonical completion, not a tool call")
	require.JSONEq(t, `{"title":"Inspect evaporator"}`, string(completion.Completion.Payload))

	response := cp.response()
	require.NoError(t, model.ValidateResponse(response))
	require.Equal(t, model.TextPart{Text: `{"title":"Inspect evaporator"}`}, response.Content[0].Parts[0])
	require.Empty(t, response.ToolCalls())
}
