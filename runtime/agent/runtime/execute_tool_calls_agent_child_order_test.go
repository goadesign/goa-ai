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
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"

	"github.com/stretchr/testify/require"
)

func TestExecuteToolCalls_AgentToolsPublishResultsAsComplete(t *testing.T) {
	recorder := &recordingHooks{ch: make(chan hooks.Event, 64)}
	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolsets:  make(map[string]ToolsetRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Bus:       recorder,
	}

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

	tool1 := tools.Ident("svc.agenttools.tool1")
	tool2 := tools.Ident("svc.agenttools.tool2")
	spec1 := newAnyJSONSpec(tool1, reg.Name)
	spec1.IsAgentTool = true
	spec1.AgentID = string(cfg.AgentID)
	spec2 := newAnyJSONSpec(tool2, reg.Name)
	spec2.IsAgentTool = true
	spec2.AgentID = string(cfg.AgentID)
	rt.toolSpecs[tool1] = spec1
	rt.toolSpecs[tool2] = spec2

	childHandles := make(chan *controlledChildHandle, 2)
	wfCtx := &testWorkflowContext{
		ctx:                    context.Background(),
		hookRuntime:            rt,
		controlledChildHandles: childHandles,
	}

	runCtx := &run.Context{
		RunID:     "run-parent",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	calls := []planner.ToolRequest{
		{
			Name:       tool1,
			RunID:      runCtx.RunID,
			SessionID:  runCtx.SessionID,
			TurnID:     runCtx.TurnID,
			ToolCallID: "call-1",
		},
		{
			Name:       tool2,
			RunID:      runCtx.RunID,
			SessionID:  runCtx.SessionID,
			TurnID:     runCtx.TurnID,
			ToolCallID: "call-2",
		},
	}

	type out struct {
		results []*planner.ToolResult
		err     error
	}
	done := make(chan out, 1)
	go func() {
		results, _, err := rt.executeToolCalls(
			wfCtx,
			"execute",
			engine.ActivityOptions{},
			runCtx.RunID,
			agent.Ident("parent.agent"),
			runCtx,
			calls,
			0,
			runCtx.TurnID,
			nil,
			time.Time{},
		)
		done <- out{results: results, err: err}
	}()

	// StartChildWorkflow is called in call order; we can release the second child first.
	h1 := <-childHandles
	h2 := <-childHandles
	close(h2.ready)
	waitForToolResult(t, recorder.ch, calls[1].ToolCallID)
	close(h1.ready)

	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.results, 2)
	require.Equal(t, calls[0].ToolCallID, got.results[0].ToolCallID)
	require.Equal(t, calls[1].ToolCallID, got.results[1].ToolCallID)

	var ends []*hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			ends = append(ends, e)
		}
	}
	require.Len(t, ends, 2)
	require.Equal(t, calls[1].ToolCallID, ends[0].ToolCallID)
	require.Equal(t, calls[0].ToolCallID, ends[1].ToolCallID)
}

func waitForToolResult(t *testing.T, ch <-chan hooks.Event, toolCallID string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case evt := <-ch:
			if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok && e.ToolCallID == toolCallID {
				return
			}
		case <-deadline.C:
			require.Fail(t, "timed out waiting for ToolResultReceivedEvent", "tool_call_id=%s", toolCallID)
			return
		}
	}
}
