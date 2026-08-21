package runtime

// This file checks final tool results before planner output crosses the
// workflow activity boundary.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestValidatePlannerFinalToolResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		final *planner.FinalToolResult
		want  string
	}{
		{
			name: "result",
			final: &planner.FinalToolResult{
				Result:      rawjson.Message(`{"status":"ok"}`),
				ResultBytes: 15,
			},
		},
		{
			name: "failure",
			final: &planner.FinalToolResult{
				Failure: &planner.ToolFailure{},
			},
		},
		{
			name: "omitted result",
			final: &planner.FinalToolResult{
				ResultOmitted:       true,
				ResultOmittedReason: "payload_limit",
			},
		},
		{
			name: "malformed result",
			final: &planner.FinalToolResult{
				Result: rawjson.Message(`{`),
			},
			want: "result is not valid JSON",
		},
		{
			name: "malformed server data",
			final: &planner.FinalToolResult{
				Result:     rawjson.Message(`{"status":"ok"}`),
				ServerData: rawjson.Message(`{`),
			},
			want: "server data is not valid JSON",
		},
		{
			name: "negative byte count",
			final: &planner.FinalToolResult{
				Result:      rawjson.Message(`{"status":"ok"}`),
				ResultBytes: -1,
			},
			want: "byte count cannot be negative",
		},
		{
			name: "failure and result",
			final: &planner.FinalToolResult{
				Result:  rawjson.Message(`{"status":"ok"}`),
				Failure: &planner.ToolFailure{},
			},
			want: "both a failure and a result",
		},
		{
			name:  "missing result",
			final: &planner.FinalToolResult{},
			want:  "missing its result",
		},
		{
			name: "omitted with result",
			final: &planner.FinalToolResult{
				Result:              rawjson.Message(`{"status":"ok"}`),
				ResultOmitted:       true,
				ResultOmittedReason: "payload_limit",
			},
			want: "marked omitted but contains a result",
		},
		{
			name: "omitted without reason",
			final: &planner.FinalToolResult{
				ResultOmitted: true,
			},
			want: "marked omitted without a reason",
		},
		{
			name: "reason without omission",
			final: &planner.FinalToolResult{
				Result:              rawjson.Message(`{"status":"ok"}`),
				ResultOmittedReason: "payload_limit",
			},
			want: "omission reason but is not omitted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePlannerFinalToolResult(test.final)
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}
