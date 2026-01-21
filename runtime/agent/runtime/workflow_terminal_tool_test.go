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
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		},
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
	require.NotNil(t, out.Final)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, terminalTool.Name, out.ToolEvents[0].Name)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "expected no planner resume/finalization after terminal tool")
}
