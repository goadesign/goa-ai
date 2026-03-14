package hooks

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestNewRunCompletedEventPreservesTemporalProviderErrorEnvelope(t *testing.T) {
	providerErr := model.NewProviderError(
		"bedrock",
		"converse_stream",
		429,
		model.ProviderErrorKindRateLimited,
		"ThrottlingException",
		"too many requests",
		"req-1",
		true,
		errors.New("throttled"),
	)

	err := WrapRunCompletionError(providerErr)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.False(t, appErr.NonRetryable())

	evt := NewRunCompletedEvent("run-1", "svc.agent", "sess-1", "failed", run.PhaseFailed, err)

	require.Equal(t, PublicErrorProviderRateLimited, evt.PublicError)
	require.Equal(t, "bedrock", evt.ErrorProvider)
	require.Equal(t, "converse_stream", evt.ErrorOperation)
	require.Equal(t, string(model.ProviderErrorKindRateLimited), evt.ErrorKind)
	require.Equal(t, "ThrottlingException", evt.ErrorCode)
	require.Equal(t, 429, evt.HTTPStatus)
	require.True(t, evt.Retryable)
}
