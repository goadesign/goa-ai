package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestExecuteToolCalls_ServiceToolsPublishResultsAsComplete(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		RunEventStore: runloginmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.slow"): newAnyJSONSpec("svc.tools.slow", "svc.tools"),
			tools.Ident("svc.tools.fast"): newAnyJSONSpec("svc.tools.fast", "svc.tools"),
		},
	}

	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		toolFutures: map[string]*controlledToolFuture{},
	}

	callSlow := planner.ToolRequest{
		Name:       tools.Ident("svc.tools.slow"),
		RunID:      "run-1",
		SessionID:  "sess-1",
		TurnID:     "turn-1",
		ToolCallID: "call-slow",
	}
	callFast := planner.ToolRequest{
		Name:       tools.Ident("svc.tools.fast"),
		RunID:      "run-1",
		SessionID:  "sess-1",
		TurnID:     "turn-1",
		ToolCallID: "call-fast",
	}

	outSlow := &ToolOutput{Payload: []byte("1")}
	outFast := &ToolOutput{Payload: []byte("2")}
	futSlow := &controlledToolFuture{ready: make(chan struct{}), out: outSlow}
	futFast := &controlledToolFuture{ready: make(chan struct{}), out: outFast}
	wfCtx.toolFutures[callSlow.ToolCallID] = futSlow
	wfCtx.toolFutures[callFast.ToolCallID] = futFast

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(futFast.ready)
		time.Sleep(10 * time.Millisecond)
		close(futSlow.ready)
	}()

	results, _, err := rt.executeToolCalls(
		wfCtx,
		"execute",
		engine.ActivityOptions{},
		"run-1",
		agent.Ident("agent-1"),
		&run.Context{RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"},
		[]planner.ToolRequest{callSlow, callFast},
		0,
		"turn-1",
		nil,
		time.Time{},
	)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Returned results remain in original call order.
	require.Equal(t, callSlow.ToolCallID, results[0].ToolCallID)
	require.Equal(t, callFast.ToolCallID, results[1].ToolCallID)

	// Streamed ToolResultReceived events should reflect completion order.
	var ends []*hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			ends = append(ends, e)
		}
	}
	require.Len(t, ends, 2)
	require.Equal(t, callFast.ToolCallID, ends[0].ToolCallID)
	require.Equal(t, callSlow.ToolCallID, ends[1].ToolCallID)

	// If the goroutine deadlocked, executeToolCalls would never return.
}

func TestExecuteToolCalls_ServiceToolErrorDoesNotAbortRun(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:           recorder,
		RunEventStore: runloginmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			tools.Ident("svc.tools.fail"): newAnyJSONSpec("svc.tools.fail", "svc.tools"),
		},
	}

	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		toolFutures: map[string]*controlledToolFuture{},
	}

	callFail := planner.ToolRequest{
		Name:       tools.Ident("svc.tools.fail"),
		RunID:      "run-1",
		SessionID:  "sess-1",
		TurnID:     "turn-1",
		ToolCallID: "call-fail",
	}

	futFail := &controlledToolFuture{
		ready: make(chan struct{}),
		err:   errors.New("activity start-to-close timeout"),
	}
	wfCtx.toolFutures[callFail.ToolCallID] = futFail
	close(futFail.ready)

	results, _, err := rt.executeToolCalls(
		wfCtx,
		"execute",
		engine.ActivityOptions{},
		"run-1",
		agent.Ident("agent-1"),
		&run.Context{RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1"},
		[]planner.ToolRequest{callFail},
		0,
		"turn-1",
		nil,
		time.Time{},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Error)
	require.Equal(t, "tool activity failed", results[0].Error.Message)
	require.NotNil(t, results[0].Error.Cause)
	require.Equal(t, "activity start-to-close timeout", results[0].Error.Cause.Message)

	var ends []*hooks.ToolResultReceivedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolResultReceivedEvent); ok {
			ends = append(ends, e)
		}
	}
	require.Len(t, ends, 1)
	require.Equal(t, callFail.ToolCallID, ends[0].ToolCallID)
	require.NotNil(t, ends[0].Error)
	require.Equal(t, "tool activity failed", ends[0].Error.Message)
}
