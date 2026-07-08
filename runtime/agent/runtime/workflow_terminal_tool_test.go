package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRunLoopStopsAfterTerminalTool(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	terminalTool := newAnyJSONSpec(tools.Ident("tasks.progress.final_report"), "tasks.progress")
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{
				Name: terminalTool.Name,
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
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
}

func TestRunLoopTerminalToolExecutesWithExhaustedBudget(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	terminalTool := newAnyJSONSpec(tools.Ident("tasks.progress.complete"), "tasks.progress")
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: terminalTool.Name}},
	}
	caps := policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		caps,
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
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
}

func TestRunLoopTerminalToolExecutesWithRetryRestriction(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	terminalTool := newAnyJSONSpec(tools.Ident("tasks.progress.complete"), "tasks.progress")
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
		Policy: &PolicyOverrides{
			RetryRestrictToTool: tools.Ident("ada.get_time_series"),
		},
	}
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: terminalTool.Name}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute"},
		input,
		base,
		initial,
		nil,
		model.TokenUsage{},
		policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 1},
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
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Equal(t, tools.Ident("ada.get_time_series"), input.Policy.RetryRestrictToTool)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
}

func TestFinalizeWithPlannerExecutesTerminalToolCall(t *testing.T) {
	out, wfCtx, terminalTool, err := runTerminalFinalization(t, nil)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Equal(t, "run-1/turn-1/attempt-2/tasks-progress-complete/0", out.ToolEvents[0].ToolCallID)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.NotNil(t, wfCtx.lastPlannerCall.Input.Finalize)
}

func TestFinalizeWithPlannerTerminalToolIgnoresRetryRestriction(t *testing.T) {
	runPolicy := &PolicyOverrides{RetryRestrictToTool: tools.Ident("ada.get_time_series")}
	out, _, terminalTool, err := runTerminalFinalization(t, runPolicy)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Nil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Equal(t, tools.Ident("ada.get_time_series"), runPolicy.RetryRestrictToTool)
}

func TestFinalizeWithPlannerTerminalToolHonorsCallerRestriction(t *testing.T) {
	out, _, _, err := runTerminalFinalization(t, &PolicyOverrides{
		RestrictToTool: tools.Ident("ada.get_time_series"),
	})

	require.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, err, `finalization terminal tool step failed on tool "runtime.tool_unavailable"`)
	require.ErrorContains(t, err, `tool "tasks.progress.complete" is not available for this run`)
}

func TestFinalizeWithPlannerRejectsPartialTerminalToolFailure(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	failTool := newAnyJSONSpec(tools.Ident("tasks.progress.fail"), "tasks.progress")
	failTool.TerminalRun = true
	failTool.Bookkeeping = true
	completeTool := newAnyJSONSpec(tools.Ident("tasks.progress.complete"), "tasks.progress")
	completeTool.TerminalRun = true
	completeTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if call.Name == failTool.Name {
				return &planner.ToolResult{
					Name:       call.Name,
					Error:      planner.NewToolError("failed terminal side effect"),
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
					Result: &planner.PlanResult{
						ToolCalls: []planner.ToolRequest{
							{Name: failTool.Name},
							{Name: completeTool.Name},
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

	out, err := rt.finalizeWithPlanner(
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
		2,
		"turn-1",
		planner.TerminationReasonFailureCap,
		time.Time{},
	)

	require.Nil(t, out)
	require.Error(t, err)
	require.ErrorContains(t, err, "finalization terminal tool step failed")
}

// runTerminalFinalization drives the finalization path where the planner returns
// the registered terminal bookkeeping tool.
func runTerminalFinalization(t *testing.T, runPolicy *PolicyOverrides) (*RunOutput, *routeWorkflowContext, tools.ToolSpec, error) {
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
	out, err := rt.finalizeWithPlanner(
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
		2,
		"turn-1",
		planner.TerminationReasonFailureCap,
		time.Time{},
	)
	return out, wfCtx, terminalTool, err
}

// newTerminalFinalizationRuntime registers the task terminal bookkeeping tool and
// routes the finalization planner turn to request that tool.
func newTerminalFinalizationRuntime(t *testing.T) (*Runtime, tools.ToolSpec, *routeWorkflowContext) {
	t.Helper()

	rt := New(WithLogger(telemetry.NoopLogger{}))
	terminalTool := newAnyJSONSpec(tools.Ident("tasks.progress.complete"), "tasks.progress")
	terminalTool.TerminalRun = true
	terminalTool.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
					Result: &planner.PlanResult{
						ToolCalls: []planner.ToolRequest{{Name: terminalTool.Name}},
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
