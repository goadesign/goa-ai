// tool_calls_test.go verifies tool-call envelope propagation across workflow and activity boundaries.
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewToolCallScheduledEventPreservesRuntimeCorrelation(t *testing.T) {
	t.Parallel()

	event := newToolCallScheduledEvent(
		"run-1",
		"svc.agent",
		"session-1",
		ToolCall{
			Name:                       "tools.continue_search",
			ToolCallID:                 "continue-1",
			Payload:                    rawjson.Message(`{"cursor":"next"}`),
			ContinuationRootToolCallID: "source-1",
		},
		"tools",
		"parent-1",
		2,
	)

	require.Equal(t, "source-1", event.ContinuationRootToolCallID)
	require.Equal(t, "parent-1", event.ParentToolCallID)
	require.Equal(t, 2, event.ExpectedChildrenTotal)
}

func TestDispatchToolCallsPropagatesLabelsToActivityInput(t *testing.T) {
	wfCtx := &testWorkflowContext{ctx: context.Background()}
	exec := &toolBatchExec{
		r: &Runtime{
			toolsets: map[string]ToolsetRegistration{
				"svc.tools": {},
			},
			toolSpecs: map[tools.Ident]tools.ToolSpec{
				"search": newAnyJSONSpec("search", "svc.tools"),
			},
		},
		activityName: "execute",
		runID:        "run-1",
		agentID:      "svc.agent",
		sessionID:    "sess-1",
		turnID:       "turn-1",
		runCtx: &run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Labels: map[string]string{
				"aura.session.id": "sess-1",
				"kind":            "brief",
			},
		},
	}

	_, err := exec.dispatchToolCalls(wfCtx, []ToolCall{{
		ToolCallID: "search-call",
		Name:       tools.Ident("search"),
		Payload:    rawjson.Message([]byte(`{"query":"status"}`)),
	}})
	require.NoError(t, err)
	require.NotNil(t, wfCtx.lastToolCall.Input)
	require.Equal(t, map[string]string{
		"aura.session.id": "sess-1",
		"kind":            "brief",
	}, wfCtx.lastToolCall.Input.Labels)
}

// TestExecuteToolCallsRetainsModelPayloadInWorkflow verifies that the activity
// receives only execution JSON while the workflow uses the retained model JSON
// for correction feedback.
func TestExecuteToolCallsRetainsModelPayloadInWorkflow(t *testing.T) {
	const (
		toolName    = tools.Ident("svc.tools.search")
		toolsetName = "svc.tools"
	)
	modelPayload := rawjson.Message(`{"query":"status"}`)
	executionPayload := rawjson.Message(`{"query":"status","credential":"server-owned"}`)
	var executedPayload rawjson.Message
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			toolsetName: {
				DecodeInExecutor: true,
				Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					executedPayload = append(rawjson.Message(nil), call.Payload...)
					return &planner.ToolResult{
						Name: call.Name,
						Failure: &planner.ToolFailure{
							Kind:  planner.FailureInvalidCall,
							Error: planner.NewToolError("query is invalid"),
							Recovery: planner.RecoveryDirective{
								Action: planner.RecoveryCorrectCall,
							},
						},
					}, nil
				}),
			},
		},
		Bus:           noopHooks{},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
	}
	seedTestToolSpecs(rt, newAnyJSONSpec(toolName, toolsetName))
	wfCtx := &testWorkflowContext{
		ctx:     context.Background(),
		runtime: rt,
	}
	call := ToolCall{
		Name:            toolName,
		ToolCallID:      "call-1",
		ModelToolCallID: "provider-call-1",
		Payload:         executionPayload,
		ModelPayload:    modelPayload,
	}

	results, _, err := rt.executeToolCalls(
		wfCtx,
		"execute",
		engine.ActivityOptions{},
		"svc.agent",
		&run.Context{RunID: "run-1", SessionID: "session-1"},
		nil,
		[]ToolCall{call},
		0,
		nil,
		time.Time{},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, executionPayload, executedPayload)
	require.NotNil(t, wfCtx.lastToolCall.Input)
	require.Equal(t, executionPayload, wfCtx.lastToolCall.Input.Payload)

	failure := results[0].ToolResult.Failure
	require.NotNil(t, failure)
	require.Equal(t, planner.RecoveryCorrectCall, failure.Recovery.Action)
	require.Equal(t, modelPayload, failure.Recovery.PriorInput)

	modelPayload[0] = '!'
	executionPayload[0] = '!'
	require.JSONEq(t, `{"query":"status","credential":"server-owned"}`, string(executedPayload))
	require.JSONEq(t, `{"query":"status"}`, string(failure.Recovery.PriorInput))
}
