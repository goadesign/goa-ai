package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
	input := rawjson.Message(`{"parameters":{}}`)
	spec := tools.ToolSpec{
		Payload: tools.TypeSpec{
			ExampleJSON: rawjson.Message(`{"devices":["crusher_1"]}`),
		},
	}

	failure := buildToolFailureFromPayloadError(validationErr, input, spec)

	require.NotNil(t, failure)
	assert.Equal(t, planner.FailureInvalidCall, failure.Kind)
	assert.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
	assert.Equal(t, input, failure.Recovery.PriorInput)
	assert.Equal(t, spec.Payload.ExampleJSON, failure.Recovery.ExampleJSON)
	require.Len(t, failure.Recovery.Issues, 1)
	assert.Equal(t, "parameters", failure.Recovery.Issues[0].Field)
}

func TestBuildToolFailureFromPayloadErrorDoesNotInventIssues(t *testing.T) {
	failure := buildToolFailureFromPayloadError(
		errors.New("payload failed generated decoding"),
		rawjson.Message(`{"value":"wrong type"}`),
		tools.ToolSpec{},
	)

	require.NotNil(t, failure)
	assert.Empty(t, failure.Recovery.Issues)
	assert.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
}
