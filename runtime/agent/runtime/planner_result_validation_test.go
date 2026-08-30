package runtime

// This file checks final tool results before planner output crosses the
// workflow activity boundary.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidatePlannerFinalToolResult(t *testing.T) {
	t.Parallel()

	var typedNil *struct{}
	typedNilCodec := tools.JSONCodec[any]{
		ToJSON: func(any) ([]byte, error) {
			return []byte(`{}`), nil
		},
		FromJSON: func([]byte) (any, error) {
			return typedNil, nil
		},
	}
	tests := []struct {
		name  string
		spec  tools.ToolSpec
		final *planner.FinalToolResult
		want  string
	}{
		{
			name:  "result-bearing tool",
			spec:  tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{Result: rawjson.Message(`{"status":"ok"}`)},
		},
		{
			name:  "failure",
			spec:  tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{Failure: &planner.ToolFailure{}},
		},
		{name: "no-result tool", final: &planner.FinalToolResult{}},
		{
			name:  "no-result tool with whitespace bytes",
			final: &planner.FinalToolResult{Result: rawjson.Message(` `)},
			want:  "does not define a result but contains one",
		},
		{
			name:  "malformed result",
			spec:  tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{Result: rawjson.Message(`{`)},
			want:  "result is not valid JSON",
		},
		{
			name:  "result decoder returns nil",
			spec:  tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{Result: rawjson.Message(`null`)},
			want:  "tool result decoded to nil",
		},
		{
			name:  "result decoder returns typed nil",
			spec:  tools.ToolSpec{Result: tools.TypeSpec{Codec: typedNilCodec}},
			final: &planner.FinalToolResult{Result: rawjson.Message(`{}`)},
			want:  "tool result decoded to nil",
		},
		{
			name: "malformed server data",
			spec: tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{
				Result:     rawjson.Message(`{"status":"ok"}`),
				ServerData: rawjson.Message(`{`),
			},
			want: "server data is not valid JSON",
		},
		{
			name: "failure and result",
			spec: tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{
				Result:  rawjson.Message(`{"status":"ok"}`),
				Failure: &planner.ToolFailure{},
			},
			want: "both a failure and a result",
		},
		{
			name:  "result-bearing tool missing result",
			spec:  tools.ToolSpec{Result: tools.TypeSpec{Codec: tools.AnyJSONCodec}},
			final: &planner.FinalToolResult{},
			want:  "tool result is missing",
		},
		{
			name:  "no-result tool with result",
			final: &planner.FinalToolResult{Result: rawjson.Message(`{"status":"ok"}`)},
			want:  "does not define a result but contains one",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePlannerFinalToolResult(test.spec, test.final)
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}
