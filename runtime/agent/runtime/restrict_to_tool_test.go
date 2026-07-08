package runtime

// restrict_to_tool_test.go verifies retry-driven tool restrictions do not leak
// beyond the correction they were created for or clear caller run policy.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestApplyToolResultPolicyHintsKeepsCallerRestrictionAfterSuccessfulToolResult(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RestrictToTool: tools.Ident("ada.resolve_time_series_sources"),
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name: tools.Ident("ada.resolve_time_series_sources"),
	}})

	assert.Equal(t, tools.Ident("ada.resolve_time_series_sources"), input.Policy.RestrictToTool)
	assert.Empty(t, input.Policy.RetryRestrictToTool)
}

func TestApplyToolResultPolicyHintsClearsSatisfiedRetryRestriction(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTool: tools.Ident("ada.resolve_time_series_sources"),
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name: tools.Ident("ada.resolve_time_series_sources"),
	}})

	assert.Empty(t, input.Policy.RetryRestrictToTool)
}

func TestApplyToolResultPolicyHintsKeepsRestrictionAfterFailedCorrection(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTool: tools.Ident("ada.resolve_time_series_sources"),
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name:  tools.Ident("ada.resolve_time_series_sources"),
		Error: planner.NewToolError("invalid arguments"),
	}})

	assert.Equal(t, tools.Ident("ada.resolve_time_series_sources"), input.Policy.RetryRestrictToTool)
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

	assert.Equal(t, tools.Ident("ada.resolve_time_series_sources"), input.Policy.RetryRestrictToTool)
}

func TestApplyToolResultPolicyHintsPromotesNewRestrictionAfterSatisfiedCorrection(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTool: tools.Ident("ada.resolve_time_series_sources"),
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{
		{
			Name: tools.Ident("ada.resolve_time_series_sources"),
		},
		{
			Name:  tools.Ident("ada.compile_query"),
			Error: planner.NewToolError("missing fields"),
			RetryHint: &planner.RetryHint{
				Tool:           tools.Ident("ada.compile_query"),
				RestrictToTool: true,
			},
		},
	})

	assert.Equal(t, tools.Ident("ada.compile_query"), input.Policy.RetryRestrictToTool)
}

func TestApplyToolResultPolicyHintsKeepsRestrictionUntilRestrictedToolSucceeds(t *testing.T) {
	t.Parallel()

	restrictedTool := tools.Ident("ada.resolve_time_series_sources")
	cases := []struct {
		name    string
		results []*planner.ToolResult
		want    tools.Ident
	}{
		{
			name: "non-terminal bookkeeping success does not clear restriction",
			results: []*planner.ToolResult{{
				Name: tools.Ident("tasks.progress.update"),
			}},
			want: restrictedTool,
		},
		{
			name: "restricted tool success clears restriction",
			results: []*planner.ToolResult{{
				Name: restrictedTool,
			}},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &RunInput{
				Policy: &PolicyOverrides{
					RetryRestrictToTool: restrictedTool,
				},
			}

			applyToolResultPolicyHints(input, tc.results)

			assert.Equal(t, tc.want, input.Policy.RetryRestrictToTool)
		})
	}
}
