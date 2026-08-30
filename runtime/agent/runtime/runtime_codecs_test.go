package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// TestExecuteToolActivity_UsesGeneratedCodecs verifies ExecuteToolActivity decodes
// and encodes using the tool_specs codecs rather than falling back to std JSON.
func TestExecuteToolActivity_UsesGeneratedCodecs(t *testing.T) {
	// Codec that ignores input and returns sentinel values so we can detect usage.
	var decodedCalled bool
	payloadCodec := tools.JSONCodec[any]{
		ToJSON: func(v any) ([]byte, error) { return json.Marshal("encoded_payload") },
		FromJSON: func(data []byte) (any, error) {
			decodedCalled = true
			require.JSONEq(t, `{"server_data":"on"}`, string(data))
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
		Payload: tools.TypeSpec{Name: "P", Codec: payloadCodec},
		Result:  tools.TypeSpec{Name: "R", Codec: resultCodec},
	}

	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.ts": {
				Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
					// Executors receive the exact model-authored JSON payload.
					require.JSONEq(t, `{"server_data":"on"}`, string(call.Payload))
					// Return arbitrary value; encode path should use result codec.
					return &planner.ToolResult{Result: map[string]string{"status": "ok"}}, nil
				}),
			},
		},
	}
	rt.toolSpecs = map[tools.Ident]tools.ToolSpec{spec.Name: spec}

	input := ToolInput{AgentID: "agent", RunID: "run", ToolsetName: "svc.ts", ToolName: spec.Name, Payload: rawjson.Message([]byte(`{"server_data":"on"}`))}
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

func TestExecuteToolActivity_RejectsEmptyPayloadAtActivityBoundary(t *testing.T) {
	var executed bool
	spec := tools.ToolSpec{
		Name: tools.Ident("svc.ts.required"),
		Payload: tools.TypeSpec{
			Name: "RequiredPayload",
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					require.Empty(t, data)
					return nil, errors.New("requiredPayload JSON is empty")
				},
			},
		},
		Result: tools.TypeSpec{
			Name: "Result",
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	}
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.ts": {
				Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
					executed = true
					return &planner.ToolResult{}, nil
				}),
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{spec.Name: spec},
	}

	out, err := rt.ExecuteToolActivity(context.Background(), &ToolInput{
		AgentID:     "agent",
		RunID:       "run",
		ToolsetName: "svc.ts",
		ToolName:    spec.Name,
	})

	require.ErrorContains(t, err, "tool payload is invalid: payload is empty")
	require.Nil(t, out)
	require.False(t, executed)
}
