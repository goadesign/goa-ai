package runtime

import (
	"context"
	"testing"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"

	"github.com/stretchr/testify/require"
)

const invokePromptText = "invoke"

func TestExecuteToolCalls_AgentToolsPublishResultsAsComplete(t *testing.T) {
	recorder := &recordingHooks{ch: make(chan hooks.Event, 64)}
	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolsets:  make(map[string]ToolsetRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       recorder,
	}

	cfg := AgentToolConfig{
		Definition: testAgentDefinition(agent.Ident("nested.agent"), "nested.workflow", "q", nil, nil),
		Name:       "svc.agenttools",

		AgentToolContent: AgentToolContent{
			Prompt: func(id tools.Ident, payload any) string {
				return invokePromptText
			},
		},
	}
	reg := NewAgentToolsetRegistration(rt, cfg)
	rt.toolsets[reg.Name] = reg

	tool1 := tools.Ident("svc.agenttools.tool1")
	tool2 := tools.Ident("svc.agenttools.tool2")
	spec1 := newAnyJSONSpec(tool1)
	spec1.IsAgentTool = true
	spec1.AgentID = string(cfg.Definition.route.ID)
	spec2 := newAnyJSONSpec(tool2)
	spec2.IsAgentTool = true
	spec2.AgentID = string(cfg.Definition.route.ID)
	seedTestToolset(rt, reg.Name, spec1, spec2)

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
	seedParentRun(t, rt.Store, runCtx.RunID, runCtx.SessionID)
	calls := []ToolCall{
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
		results []*ToolExecutionResult
		err     error
	}
	done := make(chan out, 1)
	go func() {
		results, _, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, agent.Ident("parent.agent"), runCtx, nil, calls, 0, nil, time.Time{})
		done <- out{results: results, err: err}
	}()

	// StartChildWorkflow is called in call order; we can release the second child first.
	h1 := waitForChildHandle(t, childHandles, "first child handle")
	h2 := waitForChildHandle(t, childHandles, "second child handle")
	close(h2.ready)
	waitForToolResult(t, recorder.ch, calls[1].ToolCallID)
	close(h1.ready)

	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.results, 2)
	require.NotNil(t, got.results[0].ToolResult)
	require.NotNil(t, got.results[1].ToolResult)
	require.Equal(t, calls[0].ToolCallID, got.results[0].ToolResult.ToolCallID)
	require.Equal(t, calls[1].ToolCallID, got.results[1].ToolResult.ToolCallID)

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

func TestExecuteToolCalls_CancelsAgentToolAtParentDeadline(t *testing.T) {
	t.Parallel()

	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolsets:  make(map[string]ToolsetRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       &recordingHooks{},
	}
	cfg := AgentToolConfig{
		Definition: testAgentDefinition(agent.Ident("nested.agent"), "nested.workflow", "q", nil, nil),
		Name:       "svc.agenttools",

		AgentToolContent: AgentToolContent{
			Prompt: func(tools.Ident, any) string {
				return invokePromptText
			},
		},
	}
	reg := NewAgentToolsetRegistration(rt, cfg)
	rt.toolsets[reg.Name] = reg
	tool := tools.Ident("svc.agenttools.slow")
	spec := newAnyJSONSpec(tool)
	spec.IsAgentTool = true
	spec.AgentID = string(cfg.Definition.route.ID)
	seedTestToolset(rt, reg.Name, spec)

	childHandles := make(chan *controlledChildHandle, 1)
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
	seedParentRun(t, rt.Store, runCtx.RunID, runCtx.SessionID)
	calls := []ToolCall{{
		Name:       tool,
		RunID:      runCtx.RunID,
		SessionID:  runCtx.SessionID,
		TurnID:     runCtx.TurnID,
		ToolCallID: "call-slow",
	}}

	finishBy := wfCtx.Now().Add(15 * time.Millisecond)
	type executionResult struct {
		results  []*ToolExecutionResult
		timedOut bool
		err      error
	}
	done := make(chan executionResult, 1)
	go func() {
		results, timedOut, err := rt.executeToolCalls(
			wfCtx,
			"execute",
			engine.ActivityOptions{},
			agent.Ident("parent.agent"),
			runCtx,
			nil,
			calls,
			0,
			nil,
			finishBy,
		)
		done <- executionResult{results: results, timedOut: timedOut, err: err}
	}()

	handle := waitForChildHandle(t, childHandles, "timed out child")
	require.Eventually(t, handle.wasCanceled, time.Second, time.Millisecond)
	select {
	case <-done:
		require.Fail(t, "parent returned before the child finished cancellation")
	default:
	}
	close(handle.ready)
	got := <-done
	require.NoError(t, got.err)
	require.True(t, got.timedOut)
	require.Len(t, got.results, 1)
	require.Equal(t, canceledByTimeBudgetMessage, got.results[0].ToolResult.Failure.Error.Message)
}

func TestExecuteToolCalls_WaitsForAgentChildAfterParentCancellation(t *testing.T) {
	t.Parallel()

	rt := &Runtime{
		agents:    make(map[agent.Ident]AgentRegistration),
		toolsets:  make(map[string]ToolsetRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       &recordingHooks{},
	}
	cfg := AgentToolConfig{
		Definition: testAgentDefinition(agent.Ident("nested.agent"), "nested.workflow", "q", nil, nil),
		Name:       "svc.agenttools",

		AgentToolContent: AgentToolContent{
			Prompt: func(tools.Ident, any) string {
				return invokePromptText
			},
		},
	}
	reg := NewAgentToolsetRegistration(rt, cfg)
	rt.toolsets[reg.Name] = reg
	tool := tools.Ident("svc.agenttools.cancel")
	spec := newAnyJSONSpec(tool)
	spec.IsAgentTool = true
	spec.AgentID = string(cfg.Definition.route.ID)
	seedTestToolset(rt, reg.Name, spec)

	ctx, cancel := context.WithCancel(context.Background())
	childHandles := make(chan *controlledChildHandle, 1)
	wfCtx := &testWorkflowContext{
		ctx:                    ctx,
		hookRuntime:            rt,
		controlledChildHandles: childHandles,
	}
	runCtx := &run.Context{
		RunID:     "run-parent",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	seedParentRun(t, rt.Store, runCtx.RunID, runCtx.SessionID)
	calls := []ToolCall{{
		Name:       tool,
		RunID:      runCtx.RunID,
		SessionID:  runCtx.SessionID,
		TurnID:     runCtx.TurnID,
		ToolCallID: "call-cancel",
	}}

	done := make(chan error, 1)
	go func() {
		_, _, err := rt.executeToolCalls(
			wfCtx,
			"execute",
			engine.ActivityOptions{},
			agent.Ident("parent.agent"),
			runCtx,
			nil,
			calls,
			0,
			nil,
			time.Time{},
		)
		done <- err
	}()

	handle := waitForChildHandle(t, childHandles, "canceled child")
	cancel()
	require.Eventually(t, handle.wasCanceled, time.Second, time.Millisecond)
	select {
	case <-done:
		require.Fail(t, "parent returned before the child finished cancellation")
	default:
	}
	close(handle.ready)
	require.ErrorIs(t, <-done, context.Canceled)
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

func waitForChildHandle(t *testing.T, ch <-chan *controlledChildHandle, label string) *controlledChildHandle {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	select {
	case handle := <-ch:
		return handle
	case <-deadline.C:
		require.Fail(t, "timed out waiting for child workflow handle", "label=%s", label)
		return nil
	}
}
