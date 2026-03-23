package hooks

import (
	"context"
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

	evt := NewRunCompletedEvent("run-1", "svc.agent", "sess-1", "failed", run.PhaseFailed, err, nil)

	require.NotNil(t, evt.Failure)
	require.Equal(t, PublicErrorProviderRateLimited, evt.Failure.Message)
	require.Equal(t, "bedrock", evt.Failure.Provider)
	require.Equal(t, "converse_stream", evt.Failure.Operation)
	require.Equal(t, string(model.ProviderErrorKindRateLimited), evt.Failure.Kind)
	require.Equal(t, "ThrottlingException", evt.Failure.Code)
	require.Equal(t, 429, evt.Failure.HTTPStatus)
	require.True(t, evt.Failure.Retryable)
}

func TestNewRunCompletedEventCanceledOmitsFailureMetadata(t *testing.T) {
	evt := NewRunCompletedEvent(
		"run-1",
		"svc.agent",
		"sess-1",
		"canceled",
		run.PhaseCanceled,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonUserRequested},
	)

	require.Nil(t, evt.Failure)
	require.NotNil(t, evt.Cancellation)
	require.Equal(t, run.CancellationReasonUserRequested, evt.Cancellation.Reason)
}
