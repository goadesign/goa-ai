package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestGenerateDeterministicToolCallID_UniqueAcrossAttempts(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 1, "atlas.read.get_time_series", 0)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 2, "atlas.read.get_time_series", 0)
	require.NotEqual(t, id1, id2)
}

func TestGenerateDeterministicToolCallID_DeterministicForSameInputs(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 3, "atlas.read.get_time_series", 7)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 3, "atlas.read.get_time_series", 7)
	require.Equal(t, id1, id2)
}

func TestCapFailures_SkipsToolUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []*planner.ToolResult
		want    int
	}{
		{
			name: "tool_unavailable_with_hint_does_not_decrement",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("atlas_data.atlas.discover"),
				ToolCallID: "tc1",
				Error:      planner.NewToolError("no healthy providers"),
				RetryHint: &planner.RetryHint{
					Reason: planner.RetryReasonToolUnavailable,
					Tool:   tools.Ident("atlas_data.atlas.discover"),
				},
			}},
			want: 0,
		},
		{
			name: "rate_limited_counts",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("atlas_data.atlas.discover"),
				ToolCallID: "tc2",
				Error:      planner.NewToolError("rate limited"),
				RetryHint: &planner.RetryHint{
					Reason: planner.RetryReasonRateLimited,
					Tool:   tools.Ident("atlas_data.atlas.discover"),
				},
			}},
			want: 1,
		},
		{
			name: "no_hint_counts",
			results: []*planner.ToolResult{{
				Name:       tools.Ident("atlas_data.atlas.discover"),
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
