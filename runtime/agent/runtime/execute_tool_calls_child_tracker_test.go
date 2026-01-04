package runtime

import (
	"context"
	"testing"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"

	"github.com/stretchr/testify/require"
)

func TestExecuteToolCalls_ChildTrackerUpdateEmittedOnIncrease(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"inline.ts": {
				Inline: true,
				Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
					return &planner.ToolResult{
						Name:       call.Name,
						ToolCallID: call.ToolCallID,
						Result:     "ok",
					}, nil
				},
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("inline.ts.t"): newAnyJSONSpec("inline.ts.t", "inline.ts"),
		},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           recorder,
	}

	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
	}

	parentTracker := newChildTracker("parent-tool")
	runCtx := &run.Context{
		RunID:         "child-run",
		SessionID:     "sess-1",
		TurnID:        "turn-1",
		ParentRunID:   "parent-run",
		ParentAgentID: "parent-agent",
	}

	call := func(id string) planner.ToolRequest {
		return planner.ToolRequest{
			Name:       tools.Ident("inline.ts.t"),
			RunID:      runCtx.RunID,
			SessionID:  runCtx.SessionID,
			TurnID:     runCtx.TurnID,
			ToolCallID: id,
		}
	}

	// First batch discovers 2 child IDs => one update event with total=2.
	_, _, err := rt.executeToolCalls(
		wfCtx,
		"execute",
		engine.ActivityOptions{},
		runCtx.RunID,
		agent.Ident("agent-1"),
		runCtx,
		[]planner.ToolRequest{call("c1"), call("c2")},
		0,
		runCtx.TurnID,
		parentTracker,
		time.Time{},
	)
	require.NoError(t, err)

	// Second batch discovers no new IDs => no additional update event.
	_, _, err = rt.executeToolCalls(
		wfCtx,
		"execute",
		engine.ActivityOptions{},
		runCtx.RunID,
		agent.Ident("agent-1"),
		runCtx,
		[]planner.ToolRequest{call("c1"), call("c2")},
		0,
		runCtx.TurnID,
		parentTracker,
		time.Time{},
	)
	require.NoError(t, err)

	// Third batch discovers a new ID => second update event with total=3.
	_, _, err = rt.executeToolCalls(
		wfCtx,
		"execute",
		engine.ActivityOptions{},
		runCtx.RunID,
		agent.Ident("agent-1"),
		runCtx,
		[]planner.ToolRequest{call("c1"), call("c2"), call("c3")},
		0,
		runCtx.TurnID,
		parentTracker,
		time.Time{},
	)
	require.NoError(t, err)

	var updates []*hooks.ToolCallUpdatedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallUpdatedEvent); ok {
			updates = append(updates, e)
		}
	}
	require.Len(t, updates, 2)
	require.Equal(t, "parent-run", updates[0].RunID())
	require.Equal(t, "parent-tool", updates[0].ToolCallID)
	require.Equal(t, 2, updates[0].ExpectedChildrenTotal)
	require.Equal(t, 3, updates[1].ExpectedChildrenTotal)
}
