package runtime

// confirmation_workflow_test.go verifies runtime confirmation planning semantics.
//
// Contract:
// - Runtime confirmation overrides may customize prompt and denied-result rendering.
// - The canonical execution payload on the planner tool request remains the
//   single confirmation payload published and executed by the runtime.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestConfirmationPlanOverrideKeepsCanonicalPayload(t *testing.T) {
	t.Parallel()

	rt := New()
	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			tools.Ident("tool.confirm"): {
				Prompt: func(context.Context, *planner.ToolRequest) (string, error) {
					return "Confirm tool", nil
				},
				DeniedResult: func(context.Context, *planner.ToolRequest) (any, error) {
					return map[string]string{
						"summary": "denied",
					}, nil
				},
			},
		},
	}

	call := &planner.ToolRequest{
		Name:       tools.Ident("tool.confirm"),
		ToolCallID: "tool-1",
		Payload:    rawjson.Message(`{"execution":"payload"}`),
	}

	plan, needs, err := rt.confirmationPlan(context.Background(), call)
	require.NoError(t, err)
	require.True(t, needs)
	require.NotNil(t, plan)
	require.Equal(t, "Confirm tool", plan.Prompt)
	require.JSONEq(t, `{"execution":"payload"}`, string(call.Payload.RawMessage()))
	require.Equal(t, map[string]string{"summary": "denied"}, plan.DeniedResult)
}
