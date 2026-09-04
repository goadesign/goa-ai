package planner

// This file verifies that native output-contract errors always identify the
// component whose completed output was rejected.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestNewOutputContractErrorUsesPlannerOrigin(t *testing.T) {
	cause := errors.New("invalid planner result with submitted details")
	err := NewOutputContractError(cause)

	require.Equal(t, OutputContractOriginPlanner, err.Origin())
	require.EqualError(t, err, "completed output does not meet its contract")
	require.ErrorIs(t, err, cause)
	require.NotContains(t, err.Error(), "submitted details")
}

func TestPrivateOutputContractConstructorUsesExplicitOrigin(t *testing.T) {
	err := newOutputContractErrorWithOrigin(
		errors.New("invalid model response"),
		OutputContractOriginModel,
	)

	require.Equal(t, OutputContractOriginModel, err.Origin())
}

func TestNewRecoverableModelAnswerErrorRetainsExactAnswer(t *testing.T) {
	message := &model.Message{}
	err := NewRecoverableModelAnswerError(
		errors.New("too many references"),
		&FinalResponse{Message: message},
		"Use fewer references.",
	)

	require.Equal(t, OutputContractOriginModel, err.Origin())
	require.Equal(t, ModelOutputRecoveryAnswer, err.RecoveryKind())
	require.Same(t, message, err.ModelMessage())
	require.Equal(t, "Use fewer references.", err.Correction())
}

func TestNewRecoverableModelPlanningErrorRetainsExactResponse(t *testing.T) {
	message := &model.Message{}
	err := NewRecoverableModelPlanningError(
		errors.New("selected value is not allowed"),
		message,
		"Select an exact advertised value.",
	)

	require.Equal(t, OutputContractOriginModel, err.Origin())
	require.Equal(t, ModelOutputRecoveryPlanning, err.RecoveryKind())
	require.Same(t, message, err.ModelMessage())
	require.Equal(t, "Select an exact advertised value.", err.Correction())
}
