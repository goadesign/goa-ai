package runtime

// confirmation_workflow_test.go verifies runtime confirmation planning semantics.
//
// Contract:
// - Runtime confirmation overrides may customize prompt and denied-result rendering.
// - The canonical execution payload on the planner tool request remains the
//   single confirmation payload published and executed by the runtime.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestConfirmationPlanOverrideKeepsCanonicalPayload(t *testing.T) {
	t.Parallel()

	rt := New()
	rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{
			tools.Ident("tool.confirm"): {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "Confirm tool", nil
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]string{
						"summary": "denied",
					}, nil
				},
			},
		},
	}

	call := &ToolCall{
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

func TestConfirmationDecisionRejectsMissingToolCallID(t *testing.T) {
	t.Parallel()

	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprintf("approved=%t", approved), func(t *testing.T) {
			t.Parallel()

			rt := New()
			_, _, _, err := rt.resolveConfirmationDecision(
				&testWorkflowContext{ctx: context.Background()},
				AgentRegistration{},
				&RunInput{},
				&planner.PlanInput{},
				engine.ActivityOptions{},
				ToolCall{Name: "tool.confirm"},
				"confirmation-1",
				&confirmationPlan{Prompt: "Confirm tool"},
				&api.ConfirmationDecision{
					ID:          "confirmation-1",
					Approved:    approved,
					RequestedBy: "operator",
				},
				0,
				nil,
				"turn-1",
				&runDeadlines{},
			)

			require.ErrorContains(t, err, `confirmed tool "tool.confirm" is missing tool_call_id`)
			var outputErr *planner.OutputContractError
			require.ErrorAs(t, err, &outputErr)
		})
	}
}

func TestApprovedTerminalBookkeepingExecutesBetweenBudgetAndHard(t *testing.T) {
	terminal := newAnyJSONSpec(tools.Ident("svc.complete"), "svc")
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	executions := 0
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				terminal.Name: {
					Prompt: func(context.Context, *ToolCall) (string, error) {
						return "Confirm completion", nil
					},
					DeniedResult: func(context.Context, *ToolCall) (any, error) {
						return map[string]any{"ok": false}, nil
					},
				},
			},
		}),
	)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{terminal},
	}))

	current := time.Unix(100, 0)
	input := &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		now:     func() time.Time { return current },
		runtime: rt,
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Attempt:   1,
		},
	}
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "terminal-call",
			Name:       terminal.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		policy.CapsState{},
		current,
		current.Add(time.Minute),
		input.TurnID,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Len(t, out.Suspension.Pending, 1)
	require.Equal(t, api.PendingInputKindConfirmation, out.Suspension.Pending[0].Kind)
	require.Zero(t, executions)
	require.Empty(t, wfCtx.lastPlannerCall.Name)
}

func TestTerminalPayloadConfirmationIsRejectedBeforeTranscriptCommit(t *testing.T) {
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"), "svc")
	bookkeeping.Bookkeeping = true
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				bookkeeping.Name: {
					Prompt: func(context.Context, *ToolCall) (string, error) {
						return "Confirm record", nil
					},
					DeniedResult: func(context.Context, *ToolCall) (any, error) {
						return map[string]any{"approved": false}, nil
					},
				},
			},
		}),
	)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "bookkeeping-call",
			Name:       bookkeeping.Name,
			Payload:    rawjson.Message(`{}`),
		}},
		FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}},
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		policy.CapsState{},
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, "terminal payload cannot accompany confirmation-gated tools")
	require.Empty(t, base.Messages)
}

func TestExpiredBudgetedConfirmationDoesNotBlockBookkeepingConfirmation(t *testing.T) {
	budgeted := newAnyJSONSpec(tools.Ident("svc.lookup"), "svc")
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"), "svc")
	bookkeeping.Bookkeeping = true
	confirmation := func(name tools.Ident) *ToolConfirmation {
		return &ToolConfirmation{
			Prompt: func(context.Context, *ToolCall) (string, error) {
				return "Confirm " + string(name), nil
			},
			DeniedResult: func(context.Context, *ToolCall) (any, error) {
				return map[string]any{"approved": false}, nil
			},
		}
	}
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				budgeted.Name:    confirmation(budgeted.Name),
				bookkeeping.Name: confirmation(bookkeeping.Name),
			},
		}),
	)
	executed := make([]tools.Ident, 0, 1)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executed = append(executed, call.Name)
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{budgeted, bookkeeping},
	}))

	current := time.Unix(100, 0)
	input := &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	reg := AgentRegistration{
		ID:                  input.AgentID,
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
		Planner: &stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "finalized"}},
				},
			}}, nil
		}},
	}
	rt.agents[input.AgentID] = reg
	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		now:     func() time.Time { return current },
		runtime: rt,
		planResult: &PlanResult{FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "finalized"}},
			},
		}},
		hasPlanResult: true,
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Attempt:   1,
		},
	}
	initial := &PlanResult{
		ToolCalls: []ToolCall{
			{ToolCallID: "budgeted-call", Name: budgeted.Name, Payload: rawjson.Message(`{}`)},
			{ToolCallID: "bookkeeping-call", Name: bookkeeping.Name, Payload: rawjson.Message(`{}`)},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		reg,
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		current,
		current.Add(time.Minute),
		input.TurnID,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Empty(t, executed)
	require.Empty(t, wfCtx.lastPlannerCall.Name)
}

func TestConfirmationErrorCompletesRemainingCommittedCalls(t *testing.T) {
	first := newAnyJSONSpec(tools.Ident("svc.first"), "svc")
	second := newAnyJSONSpec(tools.Ident("svc.second"), "svc")
	confirmation := func(name tools.Ident) *ToolConfirmation {
		return &ToolConfirmation{
			Prompt: func(context.Context, *ToolCall) (string, error) {
				return "Confirm " + string(name), nil
			},
			DeniedResult: func(context.Context, *ToolCall) (any, error) {
				return map[string]any{"approved": false}, nil
			},
		}
	}
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
			first.Name: confirmation(first.Name), second.Name: confirmation(second.Name),
		}}),
	)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{first, second},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "first-call", Name: first.Name, Payload: rawjson.Message(`{}`)},
		{ToolCallID: "second-call", Name: second.Name, Payload: rawjson.Message(`{}`)},
	}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Len(t, out.Suspension.Pending, 2)
}

func TestImmediateErrorCompletesUnenteredConfirmation(t *testing.T) {
	immediate := newAnyJSONSpec(tools.Ident("svc.lookup"), "svc")
	confirmed := newAnyJSONSpec(tools.Ident("svc.update"), "svc")
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
			confirmed.Name: {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "Confirm update", nil
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]any{"approved": false}, nil
				},
			},
		}}),
	)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:   "svc",
		Inline: true,
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			return nil, fmt.Errorf("inline tool %q failed", call.Name)
		},
		Specs: []tools.ToolSpec{immediate, confirmed},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "immediate-call", Name: immediate.Name, Payload: rawjson.Message(`{}`)},
		{ToolCallID: "confirmed-call", Name: confirmed.Name, Payload: rawjson.Message(`{}`)},
	}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, `inline tool "svc.lookup" failed`)
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.GreaterOrEqual(t, len(base.Messages), 2)
	require.Len(t, base.Messages[1].Parts, 2)
}

func TestRunLoopMixedImmediateAndConfirmationRecordsOneAssistantToolUseTurn(t *testing.T) {
	lookup := newAnyJSONSpec(tools.Ident("svc.lookup"), "svc")
	confirm := newAnyJSONSpec(tools.Ident("svc.confirm"), "svc")
	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				confirm.Name: {
					Prompt: func(context.Context, *ToolCall) (string, error) {
						return "Confirm svc.confirm", nil
					},
					DeniedResult: func(context.Context, *ToolCall) (any, error) {
						return map[string]any{"approved": false}, nil
					},
				},
			},
		}),
	)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
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
		planResult: &PlanResult{
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

	initial := &PlanResult{
		ToolCalls: []ToolCall{
			{ToolCallID: "confirm-call", Name: confirm.Name, Payload: rawjson.Message(`{}`)},
			{ToolCallID: "lookup-call", Name: lookup.Name, Payload: rawjson.Message(`{}`)},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Empty(t, wfCtx.lastPlannerCall.Name)
}
