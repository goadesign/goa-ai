package bedrock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type countTokensRuntimeClient struct {
	input *bedrockruntime.CountTokensInput
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
