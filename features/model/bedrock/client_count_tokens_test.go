package bedrock

import (
	"context"
	"net/http"
	"testing"

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

func TestCountTokens_UsesConverseRequestPreparation(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &Client{
		runtime:      rt,
		defaultModel: "test-model",
		maxTok:       10,
		temp:         0.5,
		think:        defaultThinkingBudget,
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
				Input: model.ToolInputFromSchema(rawjson.Message(
					`{"type":"object","properties":{"id":{"type":"string"}}}`,
				)),
			},
		},
	}

	count, err := client.CountTokens(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 42, count.InputTokens)
	require.Equal(t, "test-model", count.Model)
	require.Equal(t, model.ModelClassDefault, count.ModelClass)
	require.True(t, count.Exact)

	require.NotNil(t, rt.input)
	require.Equal(t, "test-model", *rt.input.ModelId)
	converse, ok := rt.input.Input.(*brtypes.CountTokensInputMemberConverse)
	require.True(t, ok)
	require.Len(t, converse.Value.System, 1)
	require.Len(t, converse.Value.Messages, 1)
	require.NotNil(t, converse.Value.ToolConfig)
}

// TestCountTokens_ReturnsExactCountFromPromptTooLong verifies Bedrock's
// over-window ValidationException remains an exact token-count result. The
// provider completed the measurement and reports both measured and maximum
// counts in its canonical message; history compression must receive the
// measured count rather than a provider error or local estimate.
func TestCountTokens_ReturnsExactCountFromPromptTooLong(t *testing.T) {
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
	client := &Client{
		runtime:      rt,
		defaultModel: "test-model",
		think:        defaultThinkingBudget,
	}

	count, err := client.CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassSmall,
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "oversized history"}},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 215065, count.InputTokens)
	require.Equal(t, "test-model", count.Model)
	require.Equal(t, model.ModelClassSmall, count.ModelClass)
	require.True(t, count.Exact)
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
	client := &Client{
		runtime:      rt,
		defaultModel: "test-model",
		think:        defaultThinkingBudget,
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

// TestCountTokens_OmitsThinkingBlocks verifies that replayed thinking blocks
// never reach the CountTokens API: Bedrock validates thinking signatures
// against the counting model, and signatures only verify on the model that
// issued them. Thinking-only assistant messages must be dropped entirely so
// the count input never carries empty content.
func TestCountTokens_OmitsThinkingBlocks(t *testing.T) {
	rt := &countTokensRuntimeClient{}
	client := &Client{
		runtime:      rt,
		defaultModel: "test-model",
		maxTok:       10,
		temp:         0.5,
		think:        defaultThinkingBudget,
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
				Input: model.ToolInputFromSchema(rawjson.Message(
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
	// The thinking-only assistant message is dropped; the remaining three
	// messages survive with their thinking parts removed.
	require.Len(t, converse.Value.Messages, 3)
	for _, msg := range converse.Value.Messages {
		for _, block := range msg.Content {
			_, isReasoning := block.(*brtypes.ContentBlockMemberReasoningContent)
			require.False(t, isReasoning, "count input must not contain reasoning content")
		}
	}
	// The caller's request is untouched: the assistant message still carries
	// its thinking part for the real converse call.
	require.Len(t, req.Messages, 4)
	require.Len(t, req.Messages[1].Parts, 2)
}
