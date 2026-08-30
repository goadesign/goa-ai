package runtime

// workflow_bookkeeping_contract_test.go verifies the stronger bookkeeping
// contract: bookkeeping-only batches must either finish or await in the same
// turn, or fail fast, while mixed batches still resume with only budgeted
// results.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNormalizeStepRejectsContradictoryTerminalShapes(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	budgeted := newAnyJSONSpec(tools.Ident("svc.lookup"))
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"))
	bookkeeping.Bookkeeping = true
	terminalTool := newAnyJSONSpec(tools.Ident("svc.complete"))
	terminalTool.Bookkeeping = true
	terminalTool.TerminalRun = true
	seedTestToolSpecs(rt, budgeted, bookkeeping, terminalTool)

	final := &planner.FinalResponse{
		Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		},
	}
	cases := []struct {
		name   string
		result *PlanResult
		want   string
	}{
		{
			name: "terminal plus await",
			result: &PlanResult{
				FinalResponse: final,
				Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID:       "clarify-1",
					Question: "Which item?",
				})),
			},
			want: "cannot combine terminal payload and await",
		},
		{
			name: "synthesis without tool calls",
			result: &PlanResult{
				FinalResponse:        final,
				SynthesizeAfterTools: true,
			},
			want: "synthesis-after-tools requires only tool calls",
		},
		{
			name: "synthesis with await",
			result: &PlanResult{
				ToolCalls: []ToolCall{{ToolCallID: "budgeted-call", Name: budgeted.Name}},
				Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID:       "clarify-1",
					Question: "Which item?",
				})),
				SynthesizeAfterTools: true,
			},
			want: "synthesis-after-tools requires only tool calls",
		},
		{
			name: "synthesis with bookkeeping only",
			result: &PlanResult{
				ToolCalls:            []ToolCall{{ToolCallID: "bookkeeping-call", Name: bookkeeping.Name}},
				SynthesizeAfterTools: true,
			},
			want: "synthesis-after-tools requires at least one budgeted tool",
		},
		{
			name: "synthesis with terminal tool",
			result: &PlanResult{
				ToolCalls:            []ToolCall{{ToolCallID: "terminal-call", Name: terminalTool.Name}},
				SynthesizeAfterTools: true,
			},
			want: "synthesis-after-tools cannot include terminal tool",
		},
		{
			name: "terminal plus budgeted tool",
			result: &PlanResult{
				ToolCalls:     []ToolCall{{ToolCallID: "budgeted-call", Name: budgeted.Name}},
				FinalResponse: final,
			},
			want: "cannot accompany budgeted tool",
		},
		{
			name: "terminal plus terminal tool",
			result: &PlanResult{
				ToolCalls:     []ToolCall{{ToolCallID: "terminal-call", Name: terminalTool.Name}},
				FinalResponse: final,
			},
			want: "cannot accompany terminal tool",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rt.normalizeStep(tt.result)
			var outputErr *planner.OutputContractError
			require.ErrorAs(t, err, &outputErr)
			require.ErrorContains(t, outputContractCause(t, err), tt.want)
		})
	}
}

func TestBookkeepingFailureAlwaysRequiresResume(t *testing.T) {
	t.Parallel()

	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	bookkeeping := newAnyJSONSpec(tools.Ident("svc.record"))
	bookkeeping.Bookkeeping = true
	seedTestToolSpecs(rt, bookkeeping)
	call := ToolCall{ToolCallID: "bookkeeping-call", Name: bookkeeping.Name}

	assert.False(t, rt.toolResultRequiresResume(call, &planner.ToolResult{}))
	assert.True(t, rt.toolResultRequiresResume(call, &planner.ToolResult{
		Failure: testToolFailure(planner.FailureTimeout, planner.RecoveryFinish, "timed out"),
	}))
	assert.True(t, rt.toolResultRequiresResume(call, &planner.ToolResult{
		Failure: testToolFailure(planner.FailureInvalidCall, planner.RecoveryReplan, "invalid input"),
	}))
}

func TestRunLoopBookkeepingOnlyFinalResponseFinishesWithoutResume(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("workflow.progress.set_step_status"))
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
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
			ToolCallID: "bookkeeping-call",
			Name:       bookkeeping.Name,
			Payload:    rawjson.Message(`{}`),
		}},
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "done"}},
			},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
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
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Len(t, out.ToolEvents, 1)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "bookkeeping-only final turns must not resume")
}

func TestRunLoopBookkeepingOnlyWithoutTerminalPayloadFailsFast(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("workflow.progress.set_step_status"))
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
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
			ToolCallID: "bookkeeping-call",
			Name:       bookkeeping.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.Error(t, err)
	require.Nil(t, out)
	require.Contains(t, err.Error(), "bookkeeping-only tool batch requires a terminal tool or terminal planner payload")
	require.Empty(t, wfCtx.lastPlannerCall.Name, "invalid bookkeeping-only turns must fail before resume")
}

func TestRunLoopRetryableBookkeepingTerminalFailureResumes(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	terminal := newAnyJSONSpec(tools.Ident("workflow.progress.complete"))
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure:    testToolFailure(planner.FailureInvalidCall, planner.RecoveryReplan, "report.summary length must be <= 600"),
			}, nil
		}),
		Specs: []tools.ToolSpec{terminal},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Failure: testToolFailure(planner.FailureInvalidCall, planner.RecoveryReplan, "report.summary length must be <= 600"),
		},
		planResult: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "repaired"}},
				},
			},
		},
		hasPlanResult:   true,
		recoveryCatalog: &RecoveryCatalog{Tools: []tools.Ident{terminal.Name}},
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
			Name:       terminal.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
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
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "terminal-call", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
	require.Len(t, wfCtx.lastPlannerCall.Input.Messages, 2)
	require.Equal(t, model.ConversationRoleAssistant, wfCtx.lastPlannerCall.Input.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, wfCtx.lastPlannerCall.Input.Messages[1].Role)
}

func TestRunLoopRejectsProviderToolCallWithoutID(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	_, err := createSessionForTest(context.Background(), rt.Store, "sess-1")
	require.NoError(t, err)
	agentID := agent.Ident("agent-1")
	resumeAttempts := 0
	rt.agents[agentID] = AgentRegistration{
		ID: agentID,
		Planner: &stubPlanner{resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumeAttempts++
			t.Fatal("planner must not resume after an invalid initial result")
			return nil, nil
		}},
	}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       "run-1",
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": rt.PlanResumeActivity,
		},
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
	}}
	input := &RunInput{
		AgentID: agentID, RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1",
	}
	initial := &PlanResult{ToolCalls: []ToolCall{{
		Name: tools.ToolUnavailable, Payload: rawjson.Message(`{"requested_tool":"missing.0"}`),
	}}}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ID: agentID, ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)

	require.Nil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Zero(t, resumeAttempts)
}

func TestRunLoopRejectsMultipleProviderToolCallsWithoutIDs(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	initial := &PlanResult{ToolCalls: []ToolCall{
		{Name: tools.ToolUnavailable, Payload: rawjson.Message(`{"requested_tool":"missing.one"}`)},
		{Name: tools.ToolUnavailable, Payload: rawjson.Message(`{"requested_tool":"missing.two"}`)},
	}}

	out, err := rt.runLoop(
		&testWorkflowContext{ctx: context.Background()},
		AgentRegistration{ExecuteToolActivity: "execute"},
		&RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"},
		&planner.PlanInput{RunContext: run.Context{
			RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
		}},
		initial,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.Nil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, outputContractCause(t, err), `tool "runtime.tool_unavailable" is missing tool_call_id`)
}

func TestRunLoopMixedBudgetedAndBookkeepingCarriesSynthesisOnly(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	budgeted := newAnyJSONSpec(tools.Ident("svc.tools.lookup"))
	bookkeeping := newAnyJSONSpec(tools.Ident("workflow.progress.set_step_status"))
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.tools",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"name": call.Name},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{budgeted},
	}))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))

	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		planResult:    &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
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
		ToolCalls: []ToolCall{
			{ToolCallID: "budgeted-call", Name: budgeted.Name, Payload: rawjson.Message(`{}`)},
			{ToolCallID: "bookkeeping-call", Name: bookkeeping.Name, Payload: rawjson.Message(`{}`)},
		},
		SynthesizeAfterTools: true,
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.True(t, wfCtx.lastPlannerCall.Input.SynthesisOnly)
	require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "budgeted-call", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
}

func TestRunLoopBookkeepingOnlyToolClarificationPreservesTranscriptWithoutToolOutput(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("workflow.progress.set_step_status"))
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Clarification: &ToolClarification{
					ID:       "task-input-1",
					Question: "Which record should I inspect?",
				},
			}, nil
		},
		Specs: []tools.ToolSpec{bookkeeping},
	}))
	resultJSON, err := json.Marshal(map[string]any{"ok": true})
	require.NoError(t, err)

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(resultJSON),
			Clarification: &ToolClarification{
				ID:       "task-input-1",
				Question: "Which record should I inspect?",
			},
		},
		planResult:    &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
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
	seedRunMeta(t, rt, input)
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "bookkeeping-call",
			Name:       bookkeeping.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
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

func TestRunLoopBudgetedToolClarificationRecordsResultBeforeUserAnswer(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	budgeted := newAnyJSONSpec(tools.Ident("workflow.progress.update"))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"phase": "awaiting_input"},
					ToolCallID: call.ToolCallID,
				},
				Clarification: &ToolClarification{
					ID:       "task-input-1",
					Question: "Which record group should I inspect?",
				},
			}, nil
		},
		Specs: []tools.ToolSpec{budgeted},
	}))
	resultJSON, err := json.Marshal(map[string]any{"phase": "awaiting_input"})
	require.NoError(t, err)

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(resultJSON),
			Clarification: &ToolClarification{
				ID:       "task-input-1",
				Question: "Which record group should I inspect?",
			},
		},
		planResult:    &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
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
	seedRunMeta(t, rt, input)
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "budgeted-call",
			Name:       budgeted.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
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

func TestRunLoopBookkeepingToolTerminalRejectsClarification(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("workflow.progress.set_step_status"))
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "workflow.progress",
		Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Clarification: &ToolClarification{
					ID:       "task-input-1",
					Question: "Which record should I inspect?",
				},
			}, nil
		},
		Specs: []tools.ToolSpec{bookkeeping},
	}))
	resultJSON, err := json.Marshal(map[string]any{"ok": true})
	require.NoError(t, err)

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(resultJSON),
			Clarification: &ToolClarification{
				ID:       "task-input-1",
				Question: "Which record should I inspect?",
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
	seedRunMeta(t, rt, input)
	initial := &PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "bookkeeping-call",
			Name:       bookkeeping.Name,
			Payload:    rawjson.Message(`{}`),
		}},
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "premature terminal text"}},
			},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.Error(t, err)
	require.Nil(t, out)
	require.ErrorContains(t, err, "workflow step terminal payload cannot accompany await work")
	require.Empty(t, wfCtx.lastPlannerCall.Name, "invalid tool-terminal steps must not resume")
}
