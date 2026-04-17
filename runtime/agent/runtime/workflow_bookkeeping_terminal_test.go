package runtime

// workflow_bookkeeping_terminal_test.go exercises the end-to-end invariant that
// a bookkeeping tool executes even when the run-level retrieval budget is
// exhausted. The standard terminal-commit pattern (`Bookkeeping() + TerminalRun()`)
// must complete the run cleanly in this state.

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

func TestRunLoopBookkeepingTerminalExecutesWithExhaustedBudget(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	terminal := newAnyJSONSpec(tools.Ident("tasks.complete"), "tasks.progress")
	terminal.TerminalRun = true
	terminal.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{terminal},
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
		ToolCalls: []planner.ToolRequest{{Name: terminal.Name}},
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
	require.Nil(t, out.Final, "terminal tool completions must not synthesize an empty assistant message")
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminal.Name, out.ToolEvents[0].Name)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "no planner resume/finalization expected after terminal tool")
}
