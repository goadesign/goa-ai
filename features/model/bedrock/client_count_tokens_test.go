package bedrock

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type countTokensRuntimeClient struct {
	input *bedrockruntime.CountTokensInput
	err   error
}

func (c *countTokensRuntimeClient) Converse(
	_ context.Context,
	_ *bedrockruntime.ConverseInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	return nil, nil
}

func (c *countTokensRuntimeClient) ConverseStream(
	_ context.Context,
	_ *bedrockruntime.ConverseStreamInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseStreamOutput, error) {
	return nil, nil
}

func (c *countTokensRuntimeClient) CountTokens(
	_ context.Context,
	input *bedrockruntime.CountTokensInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.CountTokensOutput, error) {
	c.input = input
	if c.err != nil {
		return nil, c.err
	}
	tokens := int32(42)
	return &bedrockruntime.CountTokensOutput{InputTokens: &tokens}, nil
}

func TestCountTokensRejectsInvalidRequestBeforeProviderCall(t *testing.T) {
	tests := []struct {
		name    string
		request *model.Request
	}{
		{name: "nil request"},
		{name: "unsupported model class", request: &model.Request{ModelClass: "unsupported"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := &countTokensRuntimeClient{}
			raw := &provider{
				runtime:      runtime,
				defaultModel: "anthropic.claude-opus-4-8",
				maxTok:       10,
				temp:         0.5,
			}
			client, newErr := model.NewClient(raw)
			require.NoError(t, newErr)

			_, err := client.CountTokens(context.Background(), tt.request)

			require.Error(t, err)
			require.Nil(t, runtime.input)
		})
	}
}

func TestCountTokens_UsesConverseRequestPreparation(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &provider{
		runtime:      rt,
		defaultModel: "anthropic.claude-opus-4-6",
		maxTok:       10,
		temp:         0.5,
	}
	req := &model.Request{
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleSystem,
				Parts: []model.Part{model.TextPart{Text: "system prompt"}},
			},
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			},
		},
		Tools: []*model.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Look up a value.",
				Input: mustBedrockToolInput(t, rawjson.Message(
					`{"type":"object","properties":{"id":{"type":"string"}}}`,
				)),
			},
		},
		Thinking: &model.ThinkingOptions{Enable: true},
	}

	count, err := client.CountTokens(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 42, count.InputTokens)
	require.Equal(t, "anthropic.claude-opus-4-6", count.Model)
	require.Equal(t, model.ModelClassDefault, count.ModelClass)
	require.True(t, count.Exact)

	require.NotNil(t, rt.input)
	require.Equal(t, "anthropic.claude-opus-4-6", *rt.input.ModelId)
	converse, ok := rt.input.Input.(*brtypes.CountTokensInputMemberConverse)
	require.True(t, ok)
	require.Len(t, converse.Value.System, 1)
	require.Len(t, converse.Value.Messages, 1)
	require.NotNil(t, converse.Value.ToolConfig)
	require.NotNil(t, converse.Value.AdditionalModelRequestFields)
	fields, err := converse.Value.AdditionalModelRequestFields.MarshalSmithyDocument()
	require.NoError(t, err)
	require.JSONEq(t, `{
		"thinking": {
			"type": "adaptive",
			"display": "summarized"
		}
	}`, string(fields))
}

// TestCountTokensUsesStrictStructuredOutputTool verifies that Opus 4.6
// Runtime counting receives the same provider-enforced tool as Converse.
func TestCountTokensUsesStrictStructuredOutputTool(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &provider{
		runtime:      rt,
		defaultModel: "global.anthropic.claude-opus-4-6-v1",
	}

	count, err := client.CountTokens(t.Context(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "judge this claim"}},
		}},
		Thinking: &model.ThinkingOptions{Enable: true},
		StructuredOutput: &model.StructuredOutput{
			Name:        "eval_judgments",
			Description: "One judgment for each supplied claim.",
			Schema: rawjson.Message(
				`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}`,
			),
		},
	})
	require.NoError(t, err)
	require.Equal(t, 42, count.InputTokens)
	require.Equal(t, "global.anthropic.claude-opus-4-6-v1", count.Model)
	require.True(t, count.Exact)
	require.NotNil(t, rt.input)
	require.Equal(t, "anthropic.claude-opus-4-6-v1", *rt.input.ModelId)
	converse, ok := rt.input.Input.(*brtypes.CountTokensInputMemberConverse)
	require.True(t, ok)
	require.NotNil(t, converse.Value.ToolConfig)
	require.Len(t, converse.Value.ToolConfig.Tools, 1)
	spec, ok := converse.Value.ToolConfig.Tools[0].(*brtypes.ToolMemberToolSpec)
	require.True(t, ok)
	require.True(t, *spec.Value.Strict)
	require.Equal(t, "eval_judgments", *spec.Value.Name)
	require.IsType(t, &brtypes.ToolChoiceMemberTool{}, converse.Value.ToolConfig.ToolChoice)
}

// TestCountTokensRejectsNativeStructuredOutput verifies that CountTokens fails
// before the provider call when Bedrock's count request cannot carry the
// OutputConfig used by Converse.
func TestCountTokensRejectsNativeStructuredOutput(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &provider{
		runtime:      rt,
		defaultModel: "anthropic.claude-opus-4-5",
	}

	count, err := client.CountTokens(t.Context(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "return JSON"}},
		}},
		StructuredOutput: &model.StructuredOutput{
			Name:   "answer",
			Schema: rawjson.Message(`{"type":"object"}`),
		},
	})

	require.Equal(t, model.TokenCount{}, count)
	require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
	require.ErrorContains(t, err, "cannot represent native structured output")
	require.Nil(t, rt.input)
}

// TestCountTokens_SendsFoundationModelID verifies that a count configured with
// a cross-region inference profile sends the backing foundation model ID on the
// wire (Runtime CountTokens rejects the profile ID), while the returned
// TokenCount still reports the configured profile ID for observability.
func TestCountTokens_SendsFoundationModelID(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &provider{
		runtime:      rt,
		defaultModel: "us.anthropic.claude-opus-4-6",
		highModel:    "us.anthropic.claude-opus-4-6",
	}

	count, err := client.CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, rt.input)
	require.Equal(t, "anthropic.claude-opus-4-6", *rt.input.ModelId)
	require.Equal(t, "us.anthropic.claude-opus-4-6", count.Model)
	require.Equal(t, model.ModelClassHighReasoning, count.ModelClass)
}

func TestCountTokensRejectsModelsThatRequireBedrockMantle(t *testing.T) {
	for _, modelID := range []string{
		"us.anthropic.claude-opus-4-7",
		"us.anthropic.claude-opus-4-8",
		"us.anthropic.claude-opus-5",
		"global.anthropic.claude-sonnet-5",
		"anthropic.claude-mythos-5",
	} {
		t.Run(modelID, func(t *testing.T) {
			rt := &countTokensRuntimeClient{}
			client := &provider{
				runtime:      rt,
				defaultModel: modelID,
			}

			count, err := client.CountTokens(t.Context(), &model.Request{
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			})

			require.Equal(t, model.TokenCount{}, count)
			require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
			require.ErrorContains(t, err, "requires the bedrock-mantle token-count endpoint")
			require.Nil(t, rt.input)
		})
	}
}

// TestCountTokensRejectsPromptTooLongMessageAsCount verifies that English
// provider text never becomes an exact token-count result.
func TestCountTokensRejectsPromptTooLongMessageAsCount(t *testing.T) {
	validationErr := &brtypes.ValidationException{
		Message: aws.String("prompt is too long: 215065 tokens > 200000 maximum"),
	}
	rt := &countTokensRuntimeClient{
		err: &smithy.OperationError{
			ServiceID:     "Bedrock Runtime",
			OperationName: "CountTokens",
			Err:           validationErr,
		},
	}
	client := &provider{
		runtime:      rt,
		defaultModel: "anthropic.claude-opus-4-6",
		smallModel:   "anthropic.claude-opus-4-6",
	}

	_, err := client.CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassSmall,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "oversized history"}},
			},
		},
	})

	require.Error(t, err)
	providerErr, ok := model.AsProviderError(err)
	require.True(t, ok)
	require.Equal(t, "ValidationException", providerErr.Code())
}

// TestCountTokens_PreservesOtherValidationErrors verifies unrecognized AWS
// validation failures retain their complete provider error contract and cause.
func TestCountTokens_PreservesOtherValidationErrors(t *testing.T) {
	validationErr := &brtypes.ValidationException{
		Message: aws.String("toolConfig.tools member must not be empty"),
	}
	responseErr := &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{
				Response: &http.Response{StatusCode: http.StatusBadRequest},
			},
			Err: validationErr,
		},
		RequestID: "request-123",
	}
	rt := &countTokensRuntimeClient{
		err: &smithy.OperationError{
			ServiceID:     "Bedrock Runtime",
			OperationName: "CountTokens",
			Err:           responseErr,
		},
	}
	client := &provider{
		runtime:      rt,
		defaultModel: "anthropic.claude-opus-4-6",
	}

	_, err := client.CountTokens(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			},
		},
	})

	require.Error(t, err)
	require.ErrorIs(t, err, validationErr)
	providerErr, ok := model.AsProviderError(err)
	require.True(t, ok)
	require.Equal(t, bedrockProviderName, providerErr.Provider())
	require.Equal(t, "count_tokens", providerErr.Operation())
	require.Equal(t, http.StatusBadRequest, providerErr.HTTPStatus())
	require.Equal(t, model.ProviderErrorKindInvalidRequest, providerErr.Kind())
	require.Equal(t, "ValidationException", providerErr.Code())
	require.Equal(t, "toolConfig.tools member must not be empty", providerErr.Message())
	require.Equal(t, "request-123", providerErr.RequestID())
	require.False(t, providerErr.Retryable())
}

// TestCountTokensPreservesThinkingBlocks verifies that CountTokens receives the
// same saved reasoning blocks as Converse.
func TestCountTokensPreservesThinkingBlocks(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &provider{
		runtime:      rt,
		defaultModel: "anthropic.claude-opus-4-6",
		maxTok:       10,
		temp:         0.5,
	}
	req := &model.Request{
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
					model.ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message(`{"id":"a"}`)},
				},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.ThinkingPart{Text: "thinking only", Signature: "sig2", Final: true},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.ToolResultPart{ToolUseID: "call-1", Content: "ok"},
				},
			},
		},
		Tools: []*model.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Look up a value.",
				Input: mustBedrockToolInput(t, rawjson.Message(
					`{"type":"object","properties":{"id":{"type":"string"}}}`,
				)),
			},
		},
	}

	count, err := client.CountTokens(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 42, count.InputTokens)

	require.NotNil(t, rt.input)
	converse, ok := rt.input.Input.(*brtypes.CountTokensInputMemberConverse)
	require.True(t, ok)
	require.Len(t, converse.Value.Messages, 4)
	reasoningBlocks := 0
	for _, msg := range converse.Value.Messages {
		for _, block := range msg.Content {
			_, isReasoning := block.(*brtypes.ContentBlockMemberReasoningContent)
			if isReasoning {
				reasoningBlocks++
			}
		}
	}
	assert.Equal(t, 2, reasoningBlocks)
	// The caller's request is untouched: the assistant message still carries
	// its thinking part for the real converse call.
	require.Len(t, req.Messages, 4)
	require.Len(t, req.Messages[1].Parts, 2)
}
