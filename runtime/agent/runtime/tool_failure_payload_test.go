package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestBuildToolFailureFromPayloadErrorPreservesGeneratedContract(t *testing.T) {
	validationErr := tools.NewValidationError(
		"unknown field parameters",
		[]*tools.FieldIssue{{
			Field:      "parameters",
			Constraint: "unknown_field",
			Allowed:    []string{"window", "devices"},
		}},
		nil,
	)

	failure := buildToolFailureFromPayloadError(validationErr)

	require.NotNil(t, failure)
	assert.Equal(t, planner.FailureInvalidCall, failure.Kind)
	assert.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
	assert.Empty(t, failure.Recovery.PriorInput)
	assert.Empty(t, failure.Recovery.ExampleJSON)
	require.Len(t, failure.Recovery.Issues, 1)
	assert.Equal(t, "parameters", failure.Recovery.Issues[0].Field)
}

func TestBuildToolFailureFromPayloadErrorDoesNotInventIssues(t *testing.T) {
	failure := buildToolFailureFromPayloadError(errors.New("payload failed generated decoding"))

	require.NotNil(t, failure)
	assert.Empty(t, failure.Recovery.Issues)
	assert.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
}
