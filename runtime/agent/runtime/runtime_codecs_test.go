package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// TestExecuteToolActivity_UsesGeneratedCodecs verifies ExecuteToolActivity decodes
// and encodes using the tool_specs codecs rather than falling back to std JSON.
func TestExecuteToolActivity_UsesGeneratedCodecs(t *testing.T) {
	// Codec that ignores input and returns sentinel values so we can detect usage.
	var decodedCalled bool
	payloadCodec := tools.JSONCodec[any]{
		ToJSON: func(v any) ([]byte, error) { return json.Marshal("encoded_payload") },
		FromJSON: func(_ []byte) (any, error) {
			decodedCalled = true
			return "decoded_payload", nil
		},
	}
	resultCodec := tools.JSONCodec[any]{
		ToJSON: func(v any) ([]byte, error) {
			return json.Marshal("encoded_result")
		},
		FromJSON: func(_ []byte) (any, error) { return "decoded_result", nil },
	}
	spec := tools.ToolSpec{
		Name:    tools.Ident("svc.ts.tool"),
		Toolset: "svc.ts",
		Payload: tools.TypeSpec{Name: "P", Codec: payloadCodec},
		Result:  tools.TypeSpec{Name: "R", Codec: resultCodec},
	}

	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.ts": {
				Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
					// Executors receive canonical JSON payloads.
					require.JSONEq(t, "{}", string(call.Payload))
					// Return arbitrary value; encode path should use result codec.
					return &planner.ToolResult{Result: map[string]string{"status": "ok"}}, nil
				},
			},
		},
	}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{spec.Name: spec}

	input := ToolInput{AgentID: "agent", RunID: "run", ToolName: spec.Name, Payload: json.RawMessage("{}")}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	// Payload codec must have been invoked for validation/decoding.
	require.True(t, decodedCalled, "expected payload codec FromJSON to be called")
	// Result encoding must come from the result codec ("encoded_result")
	var got any
	require.NoError(t, json.Unmarshal(out.Payload, &got))
	require.Equal(t, "encoded_result", got)
}
