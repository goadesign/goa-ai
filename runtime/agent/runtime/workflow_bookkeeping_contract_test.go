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

func TestRunLoopBookkeepingOnlyFinalResponseFinishesWithoutResume(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: bookkeeping.Name}},
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
		nil,
		model.TokenUsage{},
		policy.CapsState{},
		time.Time{},
		time.Time{},
		2,
		"turn-1",
		nil,
		nil,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Len(t, out.ToolEvents, 1)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "bookkeeping-only final turns must not resume")
}

func TestRunLoopBookkeepingOnlyWithoutTerminalPayloadFailsFast(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: bookkeeping.Name}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		policy.CapsState{},
		time.Time{},
		time.Time{},
		2,
		"turn-1",
		nil,
		nil,
		0,
	)
	require.Error(t, err)
	require.Nil(t, out)
	require.Contains(t, err.Error(), "bookkeeping-only tool batch requires a terminal tool or terminal planner payload")
	require.Empty(t, wfCtx.lastPlannerCall.Name, "invalid bookkeeping-only turns must fail before resume")
}

func TestRunLoopRetryableBookkeepingTerminalFailureResumes(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	terminal := newAnyJSONSpec(tools.Ident("tasks.progress.complete"), "tasks.progress")
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Error:      planner.NewToolError("brief.summary length must be <= 600"),
				RetryHint: &planner.RetryHint{
					Reason:             planner.RetryReasonInvalidArguments,
					Tool:               call.Name,
					ClarifyingQuestion: "Please resend tasks.progress.complete with a payload that satisfies: brief.summary length must be <= 600.",
				},
			}, nil
		}),
		Specs: []tools.ToolSpec{terminal},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: []byte("null"),
			Error:   "brief.summary length must be <= 600",
			RetryHint: &planner.RetryHint{
				Reason:             planner.RetryReasonInvalidArguments,
				Tool:               terminal.Name,
				ClarifyingQuestion: "Please resend tasks.progress.complete with a payload that satisfies: brief.summary length must be <= 600.",
			},
		},
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "repaired"}},
				},
			},
		},
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: terminal.Name}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		policy.CapsState{},
		time.Time{},
		time.Time{},
		2,
		"turn-1",
		nil,
		nil,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "run-1/turn-1/attempt-1/tasks-progress-complete/0", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
	require.Len(t, wfCtx.lastPlannerCall.Input.Messages, 3)
	require.Equal(t, model.ConversationRoleAssistant, wfCtx.lastPlannerCall.Input.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, wfCtx.lastPlannerCall.Input.Messages[1].Role)
	require.Equal(t, model.ConversationRoleSystem, wfCtx.lastPlannerCall.Input.Messages[2].Role)
}

func TestRunLoopMixedBudgetedAndBookkeepingStillResumes(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	budgeted := newAnyJSONSpec(tools.Ident("svc.tools.lookup"), "svc.tools")
	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.tools",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"name": call.Name},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{budgeted},
	}))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{Name: budgeted.Name},
			{Name: bookkeeping.Name},
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
		nil,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "run-1/turn-1/attempt-1/svc-tools-lookup/0", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
}

func TestRunLoopBookkeepingOnlyToolPauseAwaitsWithoutPlannerVisibleReplay(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Pause: &ToolPause{
					Clarification: &ToolPauseClarification{
						ID:       "task-input-1",
						Question: "Which alarm should I investigate?",
					},
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
			Pause: &ToolPause{
				Clarification: &ToolPauseClarification{
					ID:       "task-input-1",
					Question: "Which alarm should I investigate?",
				},
			},
		},
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}
	wfCtx.ensureSignals()
	ctrl := interrupt.NewController(wfCtx)
	wfCtx.clarifyCh <- &api.ClarificationAnswer{
		ID:     "task-input-1",
		Answer: "Check alarm 7",
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: bookkeeping.Name}},
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
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Empty(t, wfCtx.lastPlannerCall.Input.ToolOutputs, "bookkeeping pauses must not replay tool outputs into the planner")
	require.Len(t, wfCtx.lastPlannerCall.Input.Messages, 1)
	last := wfCtx.lastPlannerCall.Input.Messages[len(wfCtx.lastPlannerCall.Input.Messages)-1]
	require.Equal(t, model.ConversationRoleUser, last.Role)
	part, ok := last.Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, "Check alarm 7", part.Text)
}

func TestRunLoopBookkeepingOnlyToolPauseBeatsSameTurnFinalResponse(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Pause: &ToolPause{
					Clarification: &ToolPauseClarification{
						ID:       "task-input-1",
						Question: "Which alarm should I investigate?",
					},
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
			Pause: &ToolPause{
				Clarification: &ToolPauseClarification{
					ID:       "task-input-1",
					Question: "Which alarm should I investigate?",
				},
			},
		},
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "after wait"}}}}},
		hasPlanResult: true,
	}
	wfCtx.ensureSignals()
	ctrl := interrupt.NewController(wfCtx)
	wfCtx.clarifyCh <- &api.ClarificationAnswer{
		ID:     "task-input-1",
		Answer: "Check alarm 7",
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: bookkeeping.Name}},
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "premature terminal text"}},
			},
		},
		Streamed: true,
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
	require.Equal(t, "after wait", agentMessageText(out.Final))
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Empty(t, wfCtx.lastPlannerCall.Input.ToolOutputs, "bookkeeping pauses must not replay tool outputs into the planner")
}
