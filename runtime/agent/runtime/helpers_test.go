package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestGenerateDeterministicToolCallID_UniqueAcrossAttempts(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 1, "svc.read.get_time_series", 0)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 2, "svc.read.get_time_series", 0)
	require.NotEqual(t, id1, id2)
}

func TestGenerateDeterministicToolCallID_DeterministicForSameInputs(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)
	require.Equal(t, id1, id2)
}

func TestCapFailures_CountsEveryToolError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []*planner.ToolResult
		want    int
	}{
		{
			name: "tool_unavailable_with_hint_counts",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("svc.data.discover"),
				ToolCallID: "tc1",
				Error:      planner.NewToolError("no healthy providers"),
				RetryHint: &planner.RetryHint{
					Reason: planner.RetryReasonToolUnavailable,
					Tool:   tools.Ident("svc.data.discover"),
				},
			}},
			want: 1,
		},
		{
			name: "invalid_arguments_with_hint_counts",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("svc.data.discover"),
				ToolCallID: "tc-invalid",
				Error:      planner.NewToolError("invalid arguments"),
				RetryHint: &planner.RetryHint{
					Reason: planner.RetryReasonInvalidArguments,
					Tool:   tools.Ident("svc.data.discover"),
				},
			}},
			want: 1,
		},
		{
			name: "rate_limited_counts",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("svc.data.discover"),
				ToolCallID: "tc2",
				Error:      planner.NewToolError("rate limited"),
				RetryHint: &planner.RetryHint{
					Reason: planner.RetryReasonRateLimited,
					Tool:   tools.Ident("svc.data.discover"),
				},
			}},
			want: 1,
		},
		{
			name: "no_hint_counts",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("svc.data.discover"),
				ToolCallID: "tc3",
				Error:      planner.NewToolError("boom"),
			}},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, capFailures(tt.results))
		})
	}
}
