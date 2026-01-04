package runtime

import (
	"context"
	"testing"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"

	"github.com/stretchr/testify/require"
)

// TestExecuteToolCalls_AgentToolsFanOut verifies that multiple agent-as-tool
// calls in a single batch start child workflows in parallel (fan-out) and that
// results are merged deterministically in tool_call_id order during fan-in.
func TestExecuteToolCalls_AgentToolsFanOut(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		agents:        make(map[agent.Ident]AgentRegistration),
		toolsets:      make(map[string]ToolsetRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           recorder,
		models:        make(map[string]model.Client),
		Policy:        &stubPolicyEngine{decision: policy.Decision{Caps: policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 10}}},
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

	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		runtime:     nil, // child handle Get will not execute a real nested workflow
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
	require.NoError(t, err)
	require.Len(t, results, 2)

	// All child workflows must be started before the first Get() is invoked so
	// child executions can run in parallel at the engine level.
	require.True(t, wfCtx.sawFirstChildGet)
	require.Equal(t, len(calls), wfCtx.firstChildGetCount)

	// Results must be merged in original call order regardless of completion order.
	require.Equal(t, calls[0].ToolCallID, results[0].ToolCallID)
	require.Equal(t, calls[1].ToolCallID, results[1].ToolCallID)

	// ToolResultReceived events should be published once per tool with matching IDs.
	var toolEnds []*hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			toolEnds = append(toolEnds, e)
		}
	}
	require.Len(t, toolEnds, 2)
	seen := map[string]bool{
		toolEnds[0].ToolCallID: true,
		toolEnds[1].ToolCallID: true,
	}
	require.True(t, seen[calls[0].ToolCallID])
	require.True(t, seen[calls[1].ToolCallID])
}
