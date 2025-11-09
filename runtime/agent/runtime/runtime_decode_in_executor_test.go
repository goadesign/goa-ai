package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// TestExecuteToolActivity_DecodeInExecutor_PassesRaw verifies that when a
// toolset registration sets DecodeInExecutor=true, ExecuteToolActivity forwards
// the raw JSON payload to the executor without pre-decoding.
func TestExecuteToolActivity_DecodeInExecutor_PassesRaw(t *testing.T) {
	rt := &Runtime{toolsets: make(map[string]ToolsetRegistration)}
	// Register a toolset with DecodeInExecutor enabled.
	called := false
	rt.toolsets["svc.ts"] = ToolsetRegistration{
		Name:             "svc.ts",
		DecodeInExecutor: true,
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			called = true
			// Payload must be raw JSON to honor decode-in-executor contract.
			_, ok := call.Payload.(json.RawMessage)
			require.True(t, ok, "expected json.RawMessage in executor")
			return &planner.ToolResult{Name: tools.Ident("svc.ts.tool"), Result: map[string]any{"ok": true}}, nil
		},
		Specs: []tools.ToolSpec{{
			Name:        tools.Ident("svc.ts.tool"),
			Service:     "svc",
			Toolset:     "ts",
			Payload:     tools.TypeSpec{Name: "P"},
			Result:      tools.TypeSpec{Name: "R"},
			IsAgentTool: false,
		}},
	}

	// Call ExecuteToolActivity with a pre-encoded payload; it should flow through.
	raw := json.RawMessage(`{"x":1}`)
	input := ToolInput{ToolsetName: "svc.ts", ToolName: tools.Ident("svc.ts.tool"), Payload: raw}
	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.True(t, called)
	require.NotNil(t, out.Payload)
}
