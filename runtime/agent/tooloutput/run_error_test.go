package tooloutput

// These tests distinguish the cause that ended a real private run from earlier
// failed attempts, without changing correction policy or diagnostic retention.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

func TestRunErrorSeparatesCorrectionHistoryFromProviderFailure(t *testing.T) {
	outage := errors.New("connection lost on correction")
	provider := &recordingProvider{
		responses: []*model.Response{toolResponse(`{"value":7}`), nil},
		errors:    []error{nil, outage},
	}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	var failure *RunError
	require.ErrorAs(t, err, &failure)
	assert.Len(t, provider.requests, 2)
	require.ErrorIs(t, failure.TerminalError(), outage)
	var rejected *model.OutputValidationError
	assert.NotErrorAs(t, failure.TerminalError(), &rejected)
	require.ErrorAs(t, err, &rejected)
	assert.Contains(t, err.Error(), outage.Error())
	assert.Contains(t, err.Error(), "want string")
}

func TestRunErrorPreservesModelOutputLimitOrigin(t *testing.T) {
	response := toolResponse(`{"value":"accepted"}`)
	response.OutputLimited = true
	response.StopReason = "max_tokens"
	provider := &recordingProvider{responses: []*model.Response{response}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	var failure *RunError
	require.ErrorAs(t, err, &failure)
	var contract *planner.OutputContractError
	require.ErrorAs(t, failure.TerminalError(), &contract)
	assert.Equal(t, planner.OutputContractOriginModel, contract.Origin())
	assert.Len(t, provider.requests, 1)
	assert.Contains(t, err.Error(), "generated-output limit")
}

func TestRunErrorCorrectionExhaustionDoesNotBorrowObservedTypes(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":7}`), toolResponse(`{"value":7}`),
		toolResponse(`{"value":7}`), toolResponse(`{"value":7}`),
	}}
	_, err := Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	var failure *RunError
	require.ErrorAs(t, err, &failure)
	require.ErrorContains(t, failure.TerminalError(), "recovery_cap")
	var rejected *model.OutputValidationError
	assert.NotErrorAs(t, failure.TerminalError(), &rejected)
	var contract *planner.OutputContractError
	assert.NotErrorAs(t, failure.TerminalError(), &contract)
	require.ErrorAs(t, err, &rejected)
	assert.Len(t, provider.requests, 4)
}

func TestRunErrorPreservesMixedTerminalCauses(t *testing.T) {
	outage := errors.New("provider connection failed")
	requestContract, err := model.NewRequestContract(outputRequest())
	require.NoError(t, err)
	rejection := requestContract.RejectProviderOutput(model.OutputValidationResponseShape, nil, errors.New("invalid model result"))
	mixed := errors.Join(rejection, outage)
	provider := &recordingProvider{errors: []error{mixed}}
	_, err = Run(t.Context(), testClient(t, provider), outputRequest(), outputSpec())
	var failure *RunError
	require.ErrorAs(t, err, &failure)
	require.ErrorIs(t, failure.TerminalError(), mixed)
	require.ErrorIs(t, failure.TerminalError(), outage)
	require.ErrorIs(t, failure.TerminalError(), rejection)
	assert.Len(t, provider.requests, 1)
}

func TestRunErrorPreservesEarlyAndCancellationFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		request *model.Request
		cause   error
		calls   int
	}{
		{name: "request validation"},
		{name: "canceled", request: outputRequest(), cause: context.Canceled, calls: 1},
		{name: "deadline", request: outputRequest(), cause: context.DeadlineExceeded, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &recordingProvider{errors: []error{test.cause}}
			_, err := Run(t.Context(), testClient(t, provider), test.request, outputSpec())
			var failure *RunError
			require.ErrorAs(t, err, &failure)
			if test.cause != nil {
				require.ErrorIs(t, failure.TerminalError(), test.cause)
			} else {
				require.ErrorContains(t, failure.TerminalError(), "request is required")
			}
			assert.Len(t, provider.requests, test.calls)
		})
	}
}

func TestRunErrorKeepsExactTerminalAndDiagnosticSnapshot(t *testing.T) {
	terminal := errors.New("final connection failure")
	prior := errors.New("earlier rejected output")
	diagnostics := &runDiagnostics{}
	_, span := diagnostics.Start(t.Context(), "first attempt")
	span.RecordError(prior)
	frozen := diagnostics.failure(terminal)
	failure := &RunError{terminal: terminal, diagnostics: frozen}
	assert.Same(t, terminal, failure.TerminalError())
	assert.Same(t, frozen, failure.Unwrap())
	assert.Equal(t, frozen.Error(), failure.Error())
	span.RecordError(errors.New("late observation"))
	assert.Equal(t, frozen.Error(), failure.Error())
	require.ErrorIs(t, failure, prior)
	assert.NotContains(t, failure.Error(), "late observation")
}
