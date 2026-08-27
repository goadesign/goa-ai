package bedrock

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestBedrockOutputLimited(t *testing.T) {
	tests := []struct {
		name   string
		reason brtypes.StopReason
		want   bool
	}{
		{name: "maximum output tokens", reason: brtypes.StopReasonMaxTokens, want: true},
		{name: "model context exhausted", reason: brtypes.StopReasonModelContextWindowExceeded, want: true},
		{name: "natural end", reason: brtypes.StopReasonEndTurn},
		{name: "tool use", reason: brtypes.StopReasonToolUse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, bedrockOutputLimited(test.reason))
		})
	}
}

func TestTranslateResponse_UsageIncludesCacheTokens(t *testing.T) {
	var (
		inTokens   int32 = 100
		outTokens  int32 = 25
		total      int32 = 125
		cacheRead  int32 = 40
		cacheWrite int32 = 60
	)

	output := &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonMaxTokens,
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role:    brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: "done"}},
		}},
		Usage: &brtypes.TokenUsage{
			InputTokens:           &inTokens,
			OutputTokens:          &outTokens,
			TotalTokens:           &total,
			CacheReadInputTokens:  &cacheRead,
			CacheWriteInputTokens: &cacheWrite,
		},
	}

	resp, err := translateResponse(output, map[string]string{}, "test-model", model.ModelClassDefault)
	require.NoError(t, err)

	require.Equal(t, int(inTokens), resp.Usage.InputTokens)
	require.Equal(t, int(outTokens), resp.Usage.OutputTokens)
	require.Equal(t, int(total), resp.Usage.TotalTokens)
	require.Equal(t, int(cacheRead), resp.Usage.CacheReadTokens)
	require.Equal(t, int(cacheWrite), resp.Usage.CacheWriteTokens)
	require.Equal(t, "test-model", resp.Usage.Model)
	require.Equal(t, model.ModelClassDefault, resp.Usage.ModelClass)
	require.True(t, resp.OutputLimited)
}

func TestCompleteRejectsMissingToolCallIDWithUsage(t *testing.T) {
	inTokens := int32(7)
	outTokens := int32(2)
	totalTokens := int32(9)
	runtime := &recordingConverseRuntime{output: &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonToolUse,
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					Name:  strPtr("lookup"),
					Input: smithyDocumentFromJSON(t, `{"id":"a"}`),
				}},
			},
		}},
		Usage: &brtypes.TokenUsage{
			InputTokens:  &inTokens,
			OutputTokens: &outTokens,
			TotalTokens:  &totalTokens,
		},
	}}
	client := &provider{defaultModel: "amazon.nova-pro-v1:0", runtime: runtime}

	response, err := client.Complete(t.Context(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "look up a"}},
		}},
		Tools: []*model.ToolDefinition{{
			Name:        "lookup",
			Description: "Look up a value.",
			Input:       mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
	})

	require.Nil(t, response)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.ErrorContains(t, err, "response tool use block missing ID")
	require.Equal(t, &model.TokenUsage{
		Model:        "amazon.nova-pro-v1:0",
		InputTokens:  7,
		OutputTokens: 2,
		TotalTokens:  9,
	}, validationErr.Usage())
}
