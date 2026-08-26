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
	err := NewOutputContractError(errors.New("invalid planner result"))

	require.Equal(t, OutputContractOriginPlanner, err.Origin())
}

func TestPrivateOutputContractConstructorUsesExplicitOrigin(t *testing.T) {
	err := newOutputContractErrorWithOrigin(
		errors.New("invalid model response"),
		OutputContractOriginModel,
	)

	require.Equal(t, OutputContractOriginModel, err.Origin())
}

func TestNewRecoverableModelOutputErrorRetainsExactAnswer(t *testing.T) {
	message := &model.Message{}
	err := NewRecoverableModelOutputError(
		errors.New("too many references"),
		&FinalResponse{Message: message},
		"Use fewer references.",
	)

	require.Equal(t, OutputContractOriginModel, err.Origin())
	require.Same(t, message, err.ModelMessage())
	require.Equal(t, "Use fewer references.", err.Correction())
}
