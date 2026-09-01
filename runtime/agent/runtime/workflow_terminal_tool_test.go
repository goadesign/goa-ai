package runtime

// This file checks how terminal tools complete runs, publish final values, and
// reject contradictory planner results.

import (
	"context"
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

func TestRunLoopStopsAfterTerminalTool(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	terminalTool := newAnyJSONSpec(tools.Ident("workflow.progress.final_report"))
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{terminalTool},
	}))

	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		runtime: rt,
	}
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
		Messages:  nil,
	}
	initial := &PlanResult{
		ToolCalls: []ToolCall{
			{
				ToolCallID: "terminal-call",
				Name:       terminalTool.Name,
				Payload:    rawjson.Message(`{}`),
			},
		},
	}
	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{
			ExecuteToolActivity: "execute",
		},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Same(t, out.ToolEvents[0], out.FinalToolResult)
	require.NoError(t, validateWorkflowOutput(out, input.AgentID, input.RunID))
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
}

func TestRunLoopRejectsMixedTerminalAndNonTerminalTools(t *testing.T) {
	tests := []struct {
		name     string
		atBudget bool
	}{
		{name: "before_budget"},
		{name: "at_budget", atBudget: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
			terminal := newAnyJSONSpec(tools.Ident("svc.complete"))
			terminal.Bookkeeping = true
			terminal.TerminalRun = true
			ordinary := newAnyJSONSpec(tools.Ident("svc.lookup"))
			executions := 0
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
				Name: "svc",
				Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					executions++
					return &planner.ToolResult{
						Name:       call.Name,
						Result:     map[string]any{"ok": true},
						ToolCallID: call.ToolCallID,
					}, nil
				}),
				Specs: []tools.ToolSpec{terminal, ordinary},
			}))
			current := time.Unix(100, 0)
			budget := current.Add(time.Minute)
			if tt.atBudget {
				budget = current
			}
			wfCtx := &testWorkflowContext{
				ctx:     context.Background(),
				now:     func() time.Time { return current },
				runtime: rt,
			}
			input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
			seedRunMeta(t, rt, input)
			base := &planner.PlanInput{RunContext: run.Context{
				RunID:     input.RunID,
				SessionID: input.SessionID,
				TurnID:    input.TurnID,
				Attempt:   1,
			}}
			initial := &PlanResult{ToolCalls: []ToolCall{
				{ToolCallID: "terminal-call", Name: terminal.Name, Payload: rawjson.Message(`{}`)},
				{ToolCallID: "ordinary-call", Name: ordinary.Name, Payload: rawjson.Message(`{}`)},
			}}

			out, err := rt.runLoop(
				wfCtx,
				AgentRegistration{ExecuteToolActivity: "execute"},
				input,
				base,
				initial,
				initialCaps(RunPolicy{MaxToolCalls: 4}),
				budget,
				current.Add(2*time.Minute),
				input.TurnID,
				nil,
			)

			require.Nil(t, out)
			require.ErrorContains(t, err, "cannot mix terminal and non-terminal tools")
			require.Zero(t, executions)
			require.Empty(t, base.Messages)
		})
	}
}

func TestRunLoopRejectsTerminalToolWithPlannerAwait(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	terminal := newAnyJSONSpec(tools.Ident("svc.complete"))
	terminal.TerminalRun = true
	terminal.Bookkeeping = true
	executed := false
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executed = true
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{terminal},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID, Attempt: 1,
	}}
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "terminal-call",
			Name:       terminal.Name,
			Payload:    rawjson.Message(`{}`),
		}},
		Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarify-1", Question: "Continue?",
		})),
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, `terminal tool "svc.complete" cannot accompany planner await work`)
	require.False(t, executed)
	require.Empty(t, base.Messages)
}

func TestPolicyExcludedToolRejectsTerminalPayloadBeforeTranscriptCommit(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"))
	bookkeeping.Bookkeeping = true
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
	input := &RunInput{
		AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1",
		Policy: &PolicyOverrides{RestrictToTool: "svc.other"},
	}
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
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, outputContractCause(t, err), `planner called tool "svc.record" excluded from this run`)
	require.Empty(t, base.Messages)
}

func TestRunLoopRejectsTerminalToolClarification(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	terminal := newAnyJSONSpec(tools.Ident("svc.complete"))
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Clarification: &ToolClarification{
					ID:       "clarification-1",
					Question: "Continue?",
				},
			}, nil
		},
		Specs: []tools.ToolSpec{terminal},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "terminal-call", Name: terminal.Name, Payload: rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, `terminal tool "svc.complete" cannot request clarification`)
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.Len(t, base.Messages, 2)
}

func TestRunLoopRecordsConfirmedTerminalToolBeforeRejectingClarification(t *testing.T) {
	terminal := newAnyJSONSpec(tools.Ident("svc.complete"))
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	rt := New(newTestStore(),
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				terminal.Name: {
					Prompt: func(context.Context, *ToolCall) (string, error) {
						return "Confirm completion", nil
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
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Clarification: &ToolClarification{
					ID:       "clarification-1",
					Question: "Continue?",
				},
			}, nil
		},
		Specs: []tools.ToolSpec{terminal},
	}))
	wfCtx := &testWorkflowContext{ctx: context.Background(), runtime: rt}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	seedRunMeta(t, rt, input)
	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}}
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "terminal-call", Name: terminal.Name, Payload: rawjson.Message(`{}`),
	}}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
}

func TestRunLoopTerminalToolExecutesWithExhaustedBudget(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	terminalTool := newAnyJSONSpec(tools.Ident("workflow.progress.complete"))
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{terminalTool},
	}))

	current := time.Unix(100, 0)
	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		now:     func() time.Time { return current },
		runtime: rt,
	}
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
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "terminal-call",
			Name:       terminalTool.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}
	caps := initialCaps(RunPolicy{MaxToolCalls: 10})
	caps.RemainingToolCalls = 0
	budgetDeadline := current
	hardDeadline := current.Add(time.Minute)

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		caps,
		budgetDeadline,
		hardDeadline,
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
}

func TestRunLoopTerminalResponseBookkeepingExecutesAtBudget(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	bookkeepingTool := newAnyJSONSpec(tools.Ident("workflow.progress.record"))
	bookkeepingTool.Bookkeeping = true
	executions := 0
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeepingTool},
	}))

	current := time.Unix(100, 0)
	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		now:     func() time.Time { return current },
		runtime: rt,
	}
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
	final := &model.Message{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "done"}},
	}
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "bookkeeping-call",
			Name:       bookkeepingTool.Name,
			Payload:    rawjson.Message(`{}`),
		}},
		FinalResponse: &planner.FinalResponse{
			Message: final,
		},
	}
	budgetDeadline := current
	hardDeadline := current.Add(time.Minute)

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		budgetDeadline,
		hardDeadline,
		"turn-1",
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, final, out.Final)
	require.Equal(t, 1, executions)
	require.Len(t, out.ToolEvents, 1)
	require.Empty(t, wfCtx.lastPlannerCall.Name)
}

func TestRunLoopMixedToolCallsUseOwnedDeadlinesAtBudget(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	budgeted := newAnyJSONSpec(tools.Ident("svc.lookup"))
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"))
	bookkeeping.Bookkeeping = true
	executed := make([]tools.Ident, 0, 1)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executed = append(executed, call.Name)
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{budgeted, bookkeeping},
	}))

	current := time.Unix(100, 0)
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
	input := &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	reg := AgentRegistration{Definition: testRegistrationDefinition(input.AgentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, ExecuteToolActivity: "execute",
		ResumeActivityName: "resume",
		Planner: &stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{FinalResponse: wfCtx.planResult.FinalResponse}, nil
		}},
	}
	rt.agents[input.AgentID] = reg
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
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		current,
		current.Add(time.Minute),
		input.TurnID,
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Equal(t, []tools.Ident{bookkeeping.Name}, executed)
	require.Len(t, out.ToolEvents, 2)
	require.Equal(t, planner.FailureTimeout, out.ToolEvents[0].Failure.Kind)
	require.Nil(t, out.ToolEvents[1].Failure)
}

func TestRunLoopTerminalToolExecutesWithRetryRestriction(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	terminalTool := newAnyJSONSpec(tools.Ident("workflow.progress.complete"))
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	var executed *ToolCall
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executed = call
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{terminalTool},
	}))

	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		runtime: rt,
	}
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
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "terminal-call",
			Name:       terminalTool.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		policy.CapsState{
			MaxToolCalls:           10,
			RemainingToolCalls:     1,
			MaxRecoveryTurns:       policy.DefaultMaxRecoveryTurns,
			RemainingRecoveryTurns: policy.DefaultMaxRecoveryTurns,
		},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
	require.NotNil(t, executed)
	require.NotContains(t, executed.Labels, FinalizationReasonLabel)
}

func TestFinalizeWithPlannerExecutesTerminalToolCall(t *testing.T) {
	out, wfCtx, terminalTool, base, err := runTerminalFinalization(t, nil)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Equal(t, "terminal-final-call", out.ToolEvents[0].ToolCallID)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.NotNil(t, wfCtx.lastPlannerCall.Input.Finalize)
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.Len(t, base.Messages, 2)
}

func TestFinalizeWithPlannerRecoversRejectedModelOutput(t *testing.T) {
	for _, test := range []struct {
		name        string
		first       func() *PlanActivityOutput
		assertRetry func(*testing.T, *PlanActivityInput)
		totalTokens int
	}{
		{
			name: "provider tool JSON",
			first: func() *PlanActivityOutput {
				return &PlanActivityOutput{
					PublicationBatchID:      testPublicationBatchID,
					ModelInvocationRecovery: &ModelInvocationRecovery{Correction: "Return valid tool JSON."},
					Usage:                   model.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
				}
			},
			assertRetry: func(t *testing.T, input *PlanActivityInput) {
				t.Helper()
				require.Equal(t, "Return valid tool JSON.", input.ModelInvocationRecovery.Correction)
				require.Nil(t, input.ModelOutputRecovery)
			},
			totalTokens: 36,
		},
		{
			name: "planner output",
			first: func() *PlanActivityOutput {
				reasonSHA, reasonSize := fingerprintBytes([]byte("invalid finalization output"))
				responseSHA, responseSize := fingerprintBytes([]byte("rejected response"))
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					OutputContractFailure: &OutputContractFailure{
						Origin:                          planner.OutputContractOriginModel,
						ReasonSHA256:                    reasonSHA,
						ReasonSize:                      reasonSize,
						ModelResponsePresent:            true,
						ModelResponseSHA256:             responseSHA,
						ModelResponseFingerprintVersion: api.ModelResponseFingerprintVersionV2,
						ModelResponseSize:               responseSize,
						Correction:                      "Return the required terminal action.",
					},
					Usage: model.TokenUsage{InputTokens: 8, OutputTokens: 3, TotalTokens: 11},
				}
			},
			assertRetry: func(t *testing.T, input *PlanActivityInput) {
				t.Helper()
				require.Equal(t, "Return the required terminal action.", input.ModelOutputRecovery.Correction)
				require.Nil(t, input.ModelInvocationRecovery)
			},
			totalTokens: 35,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, terminalTool, wfCtx := newTerminalFinalizationRuntime(t)
			plannerCalls := 0
			var plannerErr error
			wfCtx.plannerRoutes["resume"] = func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				plannerCalls++
				require.NotNil(t, input.Finalize)
				require.Equal(t, planner.TerminationReasonToolFailure, input.Finalize.Reason)
				if plannerCalls == 1 {
					require.Nil(t, input.ModelInvocationRecovery)
					require.Nil(t, input.ModelOutputRecovery)
					return test.first(), plannerErr
				}
				test.assertRetry(t, input)
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					Result: &PlanResult{ToolCalls: []ToolCall{{
						ToolCallID: "terminal-replacement",
						Name:       terminalTool.Name,
						Payload:    rawjson.Message(`{}`),
					}}},
					Usage: model.TokenUsage{InputTokens: 20, OutputTokens: 4, TotalTokens: 24},
				}, plannerErr
			}
			base := &planner.PlanInput{RunContext: run.Context{
				RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
			}}
			input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}

			out, err := rt.finalizeRun(
				wfCtx,
				AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
				input,
				base,
				nil,
				nil,
				model.TokenUsage{},
				initialCaps(RunPolicy{}),
				2,
				input.TurnID,
				nil,
				planner.TerminationReasonToolFailure,
				time.Time{},
			)

			require.NoError(t, err)
			require.NotNil(t, out)
			require.Equal(t, 2, plannerCalls)
			require.Len(t, out.ToolEvents, 1)
			require.Equal(t, "terminal-replacement", out.ToolEvents[0].ToolCallID)
			require.Equal(t, test.totalTokens, out.Usage.TotalTokens)
		})
	}
}

func TestFinalizeWithPlannerStopsWhenRecoveryBudgetIsExhausted(t *testing.T) {
	rt, _, wfCtx := newTerminalFinalizationRuntime(t)
	plannerCalls := 0
	var plannerErr error
	wfCtx.plannerRoutes["resume"] = func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
		plannerCalls++
		require.NotNil(t, input.Finalize)
		return &PlanActivityOutput{
			PublicationBatchID: testPublicationBatchID,
			ModelInvocationRecovery: &ModelInvocationRecovery{
				Correction: "Return valid tool JSON.",
			},
		}, plannerErr
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
	}}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}

	out, err := rt.finalizeRun(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		policy.CapsState{MaxRecoveryTurns: 1, RemainingRecoveryTurns: 0},
		2,
		input.TurnID,
		nil,
		planner.TerminationReasonToolFailure,
		time.Time{},
	)

	require.Nil(t, out)
	require.ErrorContains(t, err, "finalization recovery turn cap exceeded")
	require.Equal(t, 1, plannerCalls)
	require.Empty(t, wfCtx.lastToolCall.Name)
}

func TestFinalizeWithPlannerRecoversCorrectableTerminalTool(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	terminalTool := newAnyJSONSpec(tools.Ident("workflow.progress.complete"))
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	executions := 0
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID}
			if executions == 1 {
				result.Failure = testToolFailure(
					planner.FailureDomainRejection,
					planner.RecoveryCorrectCall,
					"replace the unknown evidence reference",
				)
				return result, nil
			}
			result.Result = map[string]any{"ok": true}
			return result, nil
		}),
		Specs: []tools.ToolSpec{terminalTool},
	}))
	plannerCalls := 0
	var plannerErr error
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				plannerCalls++
				require.NotNil(t, input.Finalize)
				if plannerCalls == 1 {
					return &PlanActivityOutput{
						PublicationBatchID: testPublicationBatchID,
						Result: &PlanResult{ToolCalls: []ToolCall{{
							ToolCallID:      "terminal-invalid-evidence",
							ModelToolCallID: "provider-invalid-evidence",
							Name:            terminalTool.Name,
							Payload:         rawjson.Message(`{"evidence":"ev_unknown"}`),
							ModelPayload:    rawjson.Message(`{"evidence":"ev_unknown"}`),
						}}},
					}, plannerErr
				}
				require.Equal(t, []string{"terminal-invalid-evidence"}, input.RecoveryToolCallIDs)
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					RecoveryCatalog:    &RecoveryCatalog{Tools: []tools.Ident{terminalTool.Name}},
					Result: &PlanResult{ToolCalls: []ToolCall{{
						ToolCallID: "terminal-corrected-evidence",
						Name:       terminalTool.Name,
						Payload:    rawjson.Message(`{"evidence":"ev_valid"}`),
					}}},
				}, plannerErr
			},
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
				return rt.ExecuteToolActivity(ctx, input)
			},
		},
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
	}}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}

	out, err := rt.finalizeRun(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		policy.CapsState{MaxRecoveryTurns: 1, RemainingRecoveryTurns: 1},
		2,
		input.TurnID,
		nil,
		planner.TerminationReasonToolFailure,
		time.Time{},
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, 2, plannerCalls)
	require.Equal(t, 2, executions)
	require.Len(t, out.ToolEvents, 2)
	require.NotNil(t, out.ToolEvents[0].Failure)
	require.Equal(t, planner.RecoveryCorrectCall, out.ToolEvents[0].Failure.Recovery.Action)
	require.Nil(t, out.ToolEvents[1].Failure)
	require.Equal(t, "terminal-corrected-evidence", out.FinalToolResult.ToolCallID)
}

func TestFinalizeWithPlannerTerminalToolUsesRuntimeReason(t *testing.T) {
	tests := []struct {
		name          string
		runLabels     map[string]string
		policyLabels  map[string]string
		plannerLabels map[string]string
	}{
		{name: "empty labels"},
		{
			name: "conflicting labels",
			runLabels: map[string]string{
				"run":                   "run-value",
				FinalizationReasonLabel: "incorrect-run-value",
			},
			policyLabels: map[string]string{
				"policy":                "policy-value",
				FinalizationReasonLabel: "incorrect-policy-value",
			},
			plannerLabels: map[string]string{
				FinalizationReasonLabel: "incorrect-planner-value",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt, terminalTool, wfCtx := newTerminalFinalizationRuntime(t)
			var executedLabels map[string]string
			wfCtx.toolRoutes["execute"] = func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
				executedLabels = cloneLabels(input.Labels)
				return rt.ExecuteToolActivity(ctx, input)
			}
			if len(test.policyLabels) > 0 {
				rt.Policy = &stubPolicyEngine{decision: policy.Decision{
					Labels: cloneLabels(test.policyLabels),
				}}
			}
			resume := wfCtx.plannerRoutes["resume"]
			wfCtx.plannerRoutes["resume"] = func(
				ctx context.Context,
				input *PlanActivityInput,
			) (*PlanActivityOutput, error) {
				out, err := resume(ctx, input)
				if err == nil {
					out.Result.ToolCalls[0].Labels = cloneLabels(test.plannerLabels)
				}
				return out, err
			}
			base := &planner.PlanInput{
				RunContext: run.Context{
					RunID:     "run-1",
					SessionID: "sess-1",
					TurnID:    "turn-1",
					Attempt:   1,
					Labels:    cloneLabels(test.runLabels),
				},
			}
			input := &RunInput{
				AgentID:   agent.Ident("agent-1"),
				RunID:     "run-1",
				SessionID: "sess-1",
				TurnID:    "turn-1",
				Labels:    cloneLabels(test.runLabels),
			}

			out, err := rt.finalizeRun(
				wfCtx,
				AgentRegistration{
					ExecuteToolActivity: "execute",
					ResumeActivityName:  "resume",
				},
				input,
				base,
				nil,
				nil,
				model.TokenUsage{},
				initialCaps(RunPolicy{}),
				2,
				input.TurnID,
				nil,
				planner.TerminationReasonToolFailure,
				time.Time{},
			)

			require.NoError(t, err)
			require.NotNil(t, out)
			require.Len(t, out.ToolEvents, 1)
			require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
			require.NotNil(t, executedLabels)
			require.Equal(
				t,
				string(planner.TerminationReasonToolFailure),
				executedLabels[FinalizationReasonLabel],
			)
			if len(test.runLabels) == 0 {
				require.Equal(t, map[string]string{
					FinalizationReasonLabel: string(planner.TerminationReasonToolFailure),
				}, executedLabels)
			} else {
				require.Equal(t, "run-value", executedLabels["run"])
				require.Equal(t, "policy-value", executedLabels["policy"])
			}
		})
	}
}

func TestFinalizeWithPlannerTerminalToolStopsAtHard(t *testing.T) {
	rt, _, wfCtx := newTerminalFinalizationRuntime(t)
	current := time.Unix(100, 0)
	hard := current.Add(30 * time.Second)
	wfCtx.now = func() time.Time { return current }
	resume := wfCtx.plannerRoutes["resume"]
	wfCtx.plannerRoutes["resume"] = func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
		out, err := resume(ctx, input)
		current = hard
		return out, err
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}

	out, err := rt.finalizeRun(
		wfCtx,
		AgentRegistration{
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		initialCaps(RunPolicy{}),
		2,
		"turn-1",
		nil,
		planner.TerminationReasonTimeBudget,
		hard,
	)

	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, wfCtx.lastToolCall.Name)
	require.NoError(t, transcript.ValidatePlannerTranscript(base.Messages))
	require.Empty(t, base.Messages)
}

func TestFinalizeWithPlannerTerminalToolHonorsCallerRestriction(t *testing.T) {
	out, _, _, base, err := runTerminalFinalization(t, &PolicyOverrides{
		RestrictToTool: tools.Ident("catalog.lookup"),
	})

	require.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, outputContractCause(t, err), `planner called tool "workflow.progress.complete" excluded from this run`)
	require.Empty(t, base.Messages)
}

func TestFinalizeWithPlannerRejectsTerminalPayloadWithToolCalls(t *testing.T) {
	rt, terminalTool, wfCtx := newTerminalFinalizationRuntime(t)
	var plannerErr error
	wfCtx.plannerRoutes["resume"] = func(_ context.Context, _ *PlanActivityInput) (*PlanActivityOutput, error) {
		return &PlanActivityOutput{
			PublicationBatchID: testPublicationBatchID,
			Result: &PlanResult{
				ToolCalls: []ToolCall{{
					ToolCallID: "terminal-final-call",
					Name:       terminalTool.Name,
					Payload:    rawjson.Message(`{}`),
				}},
				FinalResponse: &planner.FinalResponse{Message: &model.Message{
					Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}},
				}},
			},
		}, plannerErr
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
	}}
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}

	out, err := rt.finalizeRun(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		initialCaps(RunPolicy{}),
		2,
		input.TurnID,
		nil,
		planner.TerminationReasonRecoveryCap,
		time.Time{},
	)

	require.Nil(t, out)
	require.ErrorContains(t, outputContractCause(t, err), "terminal payload cannot accompany terminal tool")
	require.Empty(t, wfCtx.lastToolCall.Name)
	require.Empty(t, base.Messages)
}

func TestFinalizeWithPlannerRejectsPartialTerminalToolFailure(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	failTool := newAnyJSONSpec(tools.Ident("workflow.progress.fail"))
	failTool.TerminalRun = true
	failTool.Bookkeeping = true
	completeTool := newAnyJSONSpec(tools.Ident("workflow.progress.complete"))
	completeTool.TerminalRun = true
	completeTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == failTool.Name {
				return &planner.ToolResult{
					Name:       call.Name,
					Failure:    testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "failed terminal side effect"),
					ToolCallID: call.ToolCallID,
				}, nil
			}
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{failTool, completeTool},
	}))

	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				if input.RunID == "" {
					return nil, context.Canceled
				}
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					Result: &PlanResult{
						ToolCalls: []ToolCall{
							{ToolCallID: "fail-call", Name: failTool.Name, Payload: rawjson.Message(`{}`)},
							{ToolCallID: "complete-call", Name: completeTool.Name, Payload: rawjson.Message(`{}`)},
						},
					},
				}, nil
			},
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
				return rt.ExecuteToolActivity(ctx, input)
			},
		},
	}
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

	out, err := rt.finalizeRun(
		wfCtx,
		AgentRegistration{
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		initialCaps(RunPolicy{}),
		2,
		"turn-1",
		nil,
		planner.TerminationReasonRecoveryCap,
		time.Time{},
	)

	require.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, err, "finalization terminal tool step failed")
}

// runTerminalFinalization drives the finalization path where the planner returns
// the registered terminal bookkeeping tool.
func runTerminalFinalization(t *testing.T, runPolicy *PolicyOverrides) (*RunOutput, *routeWorkflowContext, tools.ToolSpec, *planner.PlanInput, error) {
	t.Helper()

	rt, terminalTool, wfCtx := newTerminalFinalizationRuntime(t)
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
		Policy:    runPolicy,
	}
	out, err := rt.finalizeRun(
		wfCtx,
		AgentRegistration{
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		initialCaps(RunPolicy{}),
		2,
		"turn-1",
		nil,
		planner.TerminationReasonRecoveryCap,
		time.Time{},
	)
	return out, wfCtx, terminalTool, base, err
}

// newTerminalFinalizationRuntime registers the workflow terminal bookkeeping tool and
// routes the finalization planner turn to request that tool.
func newTerminalFinalizationRuntime(t *testing.T) (*Runtime, tools.ToolSpec, *routeWorkflowContext) {
	t.Helper()

	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	terminalTool := newAnyJSONSpec(tools.Ident("workflow.progress.complete"))
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{terminalTool},
	}))

	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				if input.RunID == "" {
					return nil, context.Canceled
				}
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					Result: &PlanResult{
						ToolCalls: []ToolCall{{
							ToolCallID: "terminal-final-call",
							Name:       terminalTool.Name,
							Payload:    rawjson.Message(`{}`),
						}},
					},
				}, nil
			},
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
				return rt.ExecuteToolActivity(ctx, input)
			},
		},
	}
	return rt, terminalTool, wfCtx
}
