package bedrock

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"goa.design/goa-ai/runtime/agent/model"
)

type errorRuntimeClient struct {
	converseErr       error
	converseStreamErr error
}

func (e *errorRuntimeClient) Converse(
	_ context.Context,
	_ *bedrockruntime.ConverseInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	return nil, e.converseErr
}

func (e *errorRuntimeClient) ConverseStream(
	_ context.Context,
	_ *bedrockruntime.ConverseStreamInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseStreamOutput, error) {
	return nil, e.converseStreamErr
}

func TestIsRateLimited_IdempotentOnSentinel(t *testing.T) {
	err := model.ErrRateLimited
	require.True(t, isRateLimited(err))

	wrapped := fmt.Errorf("provider: %w", err)
	require.True(t, isRateLimited(wrapped))
}

func TestComplete_WrapsRateLimitedErrors(t *testing.T) {
	rt := &errorRuntimeClient{
		converseErr: model.ErrRateLimited,
	}
	client := &Client{
		runtime:      rt,
		defaultModel: "test-model",
		maxTok:       10,
		temp:         0.5,
		think:        defaultThinkingBudget,
	}
	req := model.Request{
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
	}
	_, err := client.Complete(context.Background(), &req)
	require.Error(t, err)
	require.ErrorIs(t, err, model.ErrRateLimited)
}

func TestStream_WrapsRateLimitedErrors(t *testing.T) {
	rt := &errorRuntimeClient{
		converseStreamErr: model.ErrRateLimited,
	}
	client := &Client{
		runtime:      rt,
		defaultModel: "test-model",
		maxTok:       10,
		temp:         0.5,
		think:        defaultThinkingBudget,
	}
	req := model.Request{
		ModelClass: model.ModelClassDefault,
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
	}
	_, err := client.Stream(context.Background(), &req)
	require.Error(t, err)
	require.ErrorIs(t, err, model.ErrRateLimited)
}
