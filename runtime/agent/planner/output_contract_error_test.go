package planner

// This file verifies that native output-contract errors always identify the
// component whose completed output was rejected.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
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
