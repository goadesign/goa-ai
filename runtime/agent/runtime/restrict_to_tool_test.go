package runtime

// restrict_to_tool_test.go verifies retry-driven tool restrictions track every
// restricting hint in a batch, clear per tool on success, and never leak beyond
// the corrections they were created for or clear caller run policy.

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
	assert.Empty(t, input.Policy.RetryRestrictToTools)
}

func TestApplyToolResultPolicyHintsClearsSatisfiedRetryRestriction(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTools: []tools.Ident{"ada.resolve_time_series_sources"},
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name: tools.Ident("ada.resolve_time_series_sources"),
	}})

	assert.Empty(t, input.Policy.RetryRestrictToTools)
}

func TestApplyToolResultPolicyHintsKeepsRestrictionAfterFailedCorrection(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTools: []tools.Ident{"ada.resolve_time_series_sources"},
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name:  tools.Ident("ada.resolve_time_series_sources"),
		Error: planner.NewToolError("invalid arguments"),
	}})

	assert.Equal(t, []tools.Ident{"ada.resolve_time_series_sources"}, input.Policy.RetryRestrictToTools)
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

	assert.Equal(t, []tools.Ident{"ada.resolve_time_series_sources"}, input.Policy.RetryRestrictToTools)
}

func TestApplyToolResultPolicyHintsPromotesEveryRestrictingHintInBatch(t *testing.T) {
	t.Parallel()

	input := &RunInput{}

	applyToolResultPolicyHints(input, []*planner.ToolResult{
		{
			Name:  tools.Ident("ada.review_recent_changes"),
			Error: planner.NewToolError("no matching app selection"),
			RetryHint: &planner.RetryHint{
				Tool:           tools.Ident("ada.review_recent_changes"),
				RestrictToTool: true,
			},
		},
		{
			Name:  tools.Ident("ada.list_setting_changes"),
			Error: planner.NewToolError("no matching app selection"),
			RetryHint: &planner.RetryHint{
				Tool:           tools.Ident("ada.list_setting_changes"),
				RestrictToTool: true,
			},
		},
	})

	assert.Equal(t,
		[]tools.Ident{"ada.review_recent_changes", "ada.list_setting_changes"},
		input.Policy.RetryRestrictToTools,
	)
}

func TestApplyToolResultPolicyHintsClearsOnlySatisfiedMember(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTools: []tools.Ident{
				"ada.review_recent_changes",
				"ada.list_setting_changes",
			},
		},
	}

	applyToolResultPolicyHints(input, []*planner.ToolResult{{
		Name: tools.Ident("ada.review_recent_changes"),
	}})

	assert.Equal(t, []tools.Ident{"ada.list_setting_changes"}, input.Policy.RetryRestrictToTools)
}

func TestApplyToolResultPolicyHintsDoesNotPromoteToolSatisfiedInSameBatch(t *testing.T) {
	t.Parallel()

	input := &RunInput{}

	applyToolResultPolicyHints(input, []*planner.ToolResult{
		{
			Name:  tools.Ident("ada.compile_query"),
			Error: planner.NewToolError("missing fields"),
			RetryHint: &planner.RetryHint{
				Tool:           tools.Ident("ada.compile_query"),
				RestrictToTool: true,
			},
		},
		{
			Name: tools.Ident("ada.compile_query"),
		},
	})

	assert.Nil(t, input.Policy)
}

func TestApplyToolResultPolicyHintsPromotesNewRestrictionAfterSatisfiedCorrection(t *testing.T) {
	t.Parallel()

	input := &RunInput{
		Policy: &PolicyOverrides{
			RetryRestrictToTools: []tools.Ident{"ada.resolve_time_series_sources"},
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

	assert.Equal(t, []tools.Ident{"ada.compile_query"}, input.Policy.RetryRestrictToTools)
}

func TestApplyToolResultPolicyHintsKeepsRestrictionUntilRestrictedToolSucceeds(t *testing.T) {
	t.Parallel()

	restrictedTool := tools.Ident("ada.resolve_time_series_sources")
	cases := []struct {
		name    string
		results []*planner.ToolResult
		want    []tools.Ident
	}{
		{
			name: "non-terminal bookkeeping success does not clear restriction",
			results: []*planner.ToolResult{{
				Name: tools.Ident("tasks.progress.update"),
			}},
			want: []tools.Ident{restrictedTool},
		},
		{
			name: "restricted tool success clears restriction",
			results: []*planner.ToolResult{{
				Name: restrictedTool,
			}},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &RunInput{
				Policy: &PolicyOverrides{
					RetryRestrictToTools: []tools.Ident{restrictedTool},
				},
			}

			applyToolResultPolicyHints(input, tc.results)

			assert.Equal(t, tc.want, input.Policy.RetryRestrictToTools)
		})
	}
}
