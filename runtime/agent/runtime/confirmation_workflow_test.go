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
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
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

func TestRunLoopMixedImmediateAndConfirmationRecordsOneAssistantToolUseTurn(t *testing.T) {
	lookup := newAnyJSONSpec(tools.Ident("svc.lookup"), "svc")
	confirm := newAnyJSONSpec(tools.Ident("svc.confirm"), "svc")
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				confirm.Name: {
					Prompt: func(context.Context, *planner.ToolRequest) (string, error) {
						return "Confirm svc.confirm", nil
					},
					DeniedResult: func(context.Context, *planner.ToolRequest) (any, error) {
						return map[string]any{"approved": false}, nil
					},
				},
			},
		}),
	)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"name": string(call.Name),
				},
			}, nil
		}),
		Specs: []tools.ToolSpec{lookup, confirm},
	}))

	ctx := context.Background()
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)

	wfCtx := &testWorkflowContext{
		ctx: ctx,
		asyncResult: ToolOutput{
			Payload: rawjson.Message(`{"name":"ok"}`),
		},
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
		},
		hasPlanResult: true,
		hookRuntime:   rt,
	}
	wfCtx.ensureSignals()
	ctrl := interrupt.NewController(wfCtx)
	wfCtx.confirmCh <- &api.ConfirmationDecision{
		Approved:    true,
		RequestedBy: "operator",
	}

	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{Name: lookup.Name},
			{Name: confirm.Name},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		2,
		"turn-1",
		nil,
		ctrl,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)

	messages := wfCtx.lastPlannerCall.Input.Messages
	require.Len(t, messages, 2)
	require.Equal(t, model.ConversationRoleAssistant, messages[0].Role)
	require.Len(t, messages[0].Parts, 2)
	firstUse, ok := messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, string(lookup.Name), firstUse.Name)
	secondUse, ok := messages[0].Parts[1].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, string(confirm.Name), secondUse.Name)
	require.Equal(t, model.ConversationRoleUser, messages[1].Role)
	require.Len(t, messages[1].Parts, 2)
}
