package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

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

func (e *errorRuntimeClient) CountTokens(
	_ context.Context,
	_ *bedrockruntime.CountTokensInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.CountTokensOutput, error) {
	return nil, e.converseErr
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
	client := &provider{
		runtime:      rt,
		defaultModel: "test-model",
		maxTok:       10,
		temp:         0.5,
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
	providerErr, ok := model.AsProviderError(err)
	require.True(t, ok)
	require.Equal(t, bedrockProviderName, providerErr.Provider())
}

func TestStream_WrapsRateLimitedErrors(t *testing.T) {
	rt := &errorRuntimeClient{
		converseStreamErr: model.ErrRateLimited,
	}
	client := &provider{
		runtime:      rt,
		defaultModel: "test-model",
		maxTok:       10,
		temp:         0.5,
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
	providerErr, ok := model.AsProviderError(err)
	require.True(t, ok)
	require.Equal(t, bedrockProviderName, providerErr.Provider())
}

func TestWrapBedrockErrorPreservesCancellation(t *testing.T) {
	err := wrapBedrockError("converse", context.Canceled)

	require.ErrorIs(t, err, context.Canceled)
	_, ok := model.AsProviderError(err)
	require.False(t, ok)
}

func TestWrapBedrockErrorClassifiesStreamExceptionsWithoutHTTPStatus(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		status    int
		kind      model.ProviderErrorKind
		retryable bool
	}{
		{
			name:      "validation",
			err:       &brtypes.ValidationException{},
			status:    http.StatusBadRequest,
			kind:      model.ProviderErrorKindInvalidRequest,
			retryable: false,
		},
		{
			name:      "internal server",
			err:       &brtypes.InternalServerException{},
			status:    http.StatusInternalServerError,
			kind:      model.ProviderErrorKindUnavailable,
			retryable: true,
		},
		{
			name:      "service unavailable",
			err:       &brtypes.ServiceUnavailableException{},
			status:    http.StatusServiceUnavailable,
			kind:      model.ProviderErrorKindUnavailable,
			retryable: true,
		},
		{
			name:      "model timeout",
			err:       &brtypes.ModelTimeoutException{},
			status:    http.StatusRequestTimeout,
			kind:      model.ProviderErrorKindUnavailable,
			retryable: true,
		},
		{
			name:      "model stream error",
			err:       &brtypes.ModelStreamErrorException{},
			status:    424,
			kind:      model.ProviderErrorKindUnavailable,
			retryable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := wrapBedrockError("converse_stream.recv", tt.err)

			providerErr, ok := model.AsProviderError(err)
			require.True(t, ok)
			require.Equal(t, tt.status, providerErr.HTTPStatus())
			require.Equal(t, tt.kind, providerErr.Kind())
			require.Equal(t, tt.retryable, providerErr.Retryable())
		})
	}
}
