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

func TestExecuteToolCalls_MixedBatch_DoesNotRegressOrderingWithinCategories(t *testing.T) {
	recorder := &recordingHooks{ch: make(chan hooks.Event, 128)}
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
			"inline.ts": {
				Inline: true,
				Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
					return &planner.ToolResult{
						Name:       call.Name,
						ToolCallID: call.ToolCallID,
						Result:     "inline",
					}, nil
				},
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.a1"):     newAnyJSONSpec("svc.tools.a1", "svc.tools"),
			tools.Ident("svc.tools.a2"):     newAnyJSONSpec("svc.tools.a2", "svc.tools"),
			tools.Ident("inline.ts.inline"): newAnyJSONSpec("inline.ts.inline", "inline.ts"),
			tools.Ident("svc.agent.child"): func() tools.ToolSpec {
				spec := newAnyJSONSpec("svc.agent.child", "svc.agenttools")
				spec.IsAgentTool = true
				spec.AgentID = "nested.agent"
				return spec
			}(),
		},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           recorder,
	}

	// Register the agent toolset that maps svc.agenttools.* to child workflows.
	cfg := AgentToolConfig{
		AgentID: agent.Ident("nested.agent"),
		Name:    "svc.agenttools",
		Route: AgentRoute{
			ID:               agent.Ident("nested.agent"),
			WorkflowName:     "nested.workflow",
			DefaultTaskQueue: "q",
		},
	}
	reg := NewAgentToolsetRegistration(rt, cfg)
	rt.toolsets[reg.Name] = reg

	childHandles := make(chan *controlledChildHandle, 1)
	act1 := &controlledToolFuture{ready: make(chan struct{}), out: &ToolOutput{Payload: []byte("1")}}
	act2 := &controlledToolFuture{ready: make(chan struct{}), out: &ToolOutput{Payload: []byte("2")}}
	wfCtx := &testWorkflowContext{
		ctx:                    context.Background(),
		hookRuntime:            rt,
		toolFutures:            map[string]*controlledToolFuture{"call-a1": act1, "call-a2": act2},
		controlledChildHandles: childHandles,
	}

	runCtx := &run.Context{RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"}
	calls := []planner.ToolRequest{
		{Name: tools.Ident("inline.ts.inline"), RunID: runCtx.RunID, SessionID: runCtx.SessionID, TurnID: runCtx.TurnID, ToolCallID: "call-inline"},
		{Name: tools.Ident("svc.tools.a1"), RunID: runCtx.RunID, SessionID: runCtx.SessionID, TurnID: runCtx.TurnID, ToolCallID: "call-a1"},
		{Name: tools.Ident("svc.tools.a2"), RunID: runCtx.RunID, SessionID: runCtx.SessionID, TurnID: runCtx.TurnID, ToolCallID: "call-a2"},
		{Name: tools.Ident("svc.agent.child"), RunID: runCtx.RunID, SessionID: runCtx.SessionID, TurnID: runCtx.TurnID, ToolCallID: "call-child"},
	}

	type out struct {
		results  []*planner.ToolResult
		timedOut bool
		err      error
	}
	done := make(chan out, 1)
	go func() {
		results, timedOut, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, agent.Ident("agent-1"), runCtx, calls, 0, nil, time.Time{})
		done <- out{results: results, timedOut: timedOut, err: err}
	}()

	// Inline result is emitted during dispatch. Now control activity readiness to ensure
	// activity results are streamed in readiness order (a2 then a1).
	close(act2.ready)
	waitForToolResult(t, recorder.ch, "call-a2")
	close(act1.ready)
	waitForToolResult(t, recorder.ch, "call-a1")

	// Child result is collected after activities; release it last to avoid hanging.
	child := <-childHandles
	close(child.ready)

	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.results, 4)

	var ends []*hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			ends = append(ends, e)
		}
	}
	require.Len(t, ends, 4)

	// Inline is emitted immediately.
	require.Equal(t, "call-inline", ends[0].ToolCallID)
	// Activity results are streamed in readiness order (a2 then a1).
	require.Equal(t, "call-a2", ends[1].ToolCallID)
	require.Equal(t, "call-a1", ends[2].ToolCallID)
	// Child result comes after activities (current behavior).
	require.Equal(t, "call-child", ends[3].ToolCallID)
}
