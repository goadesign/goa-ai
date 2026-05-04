package runtime

// restrict_to_tool_test.go verifies retry-driven tool restrictions do not leak
// beyond the correction they were created for.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestApplyToolResultPolicyHintsClearsSatisfiedRestriction(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RestrictToTool: tools.Ident("ada.resolve_time_series_sources"),
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name: tools.Ident("ada.resolve_time_series_sources"),
	}})

	assert.Empty(t, input.Policy.RestrictToTool)
}

func TestApplyToolResultPolicyHintsKeepsRestrictionAfterFailedCorrection(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RestrictToTool: tools.Ident("ada.resolve_time_series_sources"),
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name:  tools.Ident("ada.resolve_time_series_sources"),
		Error: planner.NewToolError("invalid arguments"),
	}})

	assert.Equal(t, tools.Ident("ada.resolve_time_series_sources"), input.Policy.RestrictToTool)
}

func TestApplyToolResultPolicyHintsPromotesNewRestriction(t *testing.T) {
	t.Parallel()

	input := &RunInput{}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name:  tools.Ident("ada.resolve_time_series_sources"),
		Error: planner.NewToolError("missing fields"),
		RetryHint: &planner.RetryHint{
			Tool:           tools.Ident("ada.resolve_time_series_sources"),
			RestrictToTool: true,
		},
	}})

	assert.Equal(t, tools.Ident("ada.resolve_time_series_sources"), input.Policy.RestrictToTool)
}
