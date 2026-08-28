package hooks

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestNewRunCompletedEventPreservesProviderRetryability(t *testing.T) {
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

	err := temporalerrors.Wrap(providerErr)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.False(t, appErr.NonRetryable())

	evt := NewRunCompletedEvent("run-1", "svc.agent", "sess-1", "failed", run.PhaseFailed, nil, err, nil)

	require.NotNil(t, evt.Failure)
	require.Equal(t, PublicErrorProviderRateLimited, evt.Failure.Message)
	require.Equal(t, "bedrock", evt.Failure.Provider)
	require.Equal(t, "converse_stream", evt.Failure.Operation)
	require.Equal(t, string(model.ProviderErrorKindRateLimited), evt.Failure.Kind)
	require.Equal(t, "ThrottlingException", evt.Failure.Code)
	require.Equal(t, 429, evt.Failure.HTTPStatus)
	require.True(t, evt.Failure.Retryable)
}

func TestNewRunCompletedEventPreservesPlannerOutputRetryability(t *testing.T) {
	outputErr := planner.NewOutputContractError(errors.New("missing required citation"))

	err := temporalerrors.Wrap(outputErr)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.True(t, appErr.NonRetryable())

	evt := NewRunCompletedEvent("run-1", "svc.agent", "sess-1", "failed", run.PhaseFailed, nil, err, nil)

	require.NotNil(t, evt.Failure)
	require.Equal(t, PublicErrorOutputContract, evt.Failure.Message)
	require.Equal(t, ErrorKindPlannerOutput, evt.Failure.Kind)
	require.False(t, evt.Failure.Retryable)
	require.Empty(t, evt.Failure.Provider)
	require.Empty(t, evt.Failure.Operation)
	require.Empty(t, evt.Failure.Code)
	require.Zero(t, evt.Failure.HTTPStatus)
	require.Contains(t, evt.Failure.DebugMessage, "completed output does not meet its contract")
	require.NotContains(t, evt.Failure.DebugMessage, "missing required citation")
}

func TestNewRunCompletedEventPreservesModelOutputOrigin(t *testing.T) {
	outputErr := outputcontract.NewWithOrigin(
		errors.New("invalid tool payload"),
		outputcontract.OriginModel,
	)
	err := temporalerrors.Wrap(outputErr)

	evt := NewRunCompletedEvent("run-1", "svc.agent", "sess-1", "failed", run.PhaseFailed, nil, err, nil)

	require.NotNil(t, evt.Failure)
	require.Equal(t, PublicErrorOutputContract, evt.Failure.Message)
	require.Equal(t, ErrorKindModelOutput, evt.Failure.Kind)
	require.False(t, evt.Failure.Retryable)
	require.Contains(t, evt.Failure.DebugMessage, "completed output does not meet its contract")
	require.NotContains(t, evt.Failure.DebugMessage, "invalid tool payload")
}

func TestNewRunCompletedEventCanceledOmitsFailureMetadata(t *testing.T) {
	evt := NewRunCompletedEvent(
		"run-1",
		"svc.agent",
		"sess-1",
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonUserRequested},
	)

	require.Nil(t, evt.Failure)
	require.NotNil(t, evt.Cancellation)
	require.Equal(t, run.CancellationReasonUserRequested, evt.Cancellation.Reason)
}

func TestNewRunCompletedEventCarriesRunLabels(t *testing.T) {
	labels := map[string]string{"household_id": "house-42"}
	evt := NewRunCompletedEvent("run-1", "svc.agent", "sess-1", "success", run.PhaseCompleted, labels, nil, nil)

	require.Equal(t, labels, evt.Labels)
}

func TestNewPlannerNoteEventOwnsLabels(t *testing.T) {
	labels := map[string]string{"phase": "planning"}
	evt := NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "checking inputs", labels)

	labels["phase"] = "changed"

	require.Equal(t, "planning", evt.Labels["phase"])
}
