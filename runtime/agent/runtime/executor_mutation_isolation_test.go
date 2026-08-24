package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestExecuteToolActivityIsolatesExecutorMutation(t *testing.T) {
	const (
		toolName = tools.Ident("svc.tools.lookup")
		toolset  = "svc.tools"
	)
	var executorResult *planner.ToolResult
	var materialized ToolCall
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		toolsets: map[string]ToolsetRegistration{
			toolset: {
				Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					mutateEveryToolCallField(call)
					executorResult = &planner.ToolResult{
						Name:       call.Name,
						ToolCallID: call.ToolCallID,
						Result:     map[string]any{"status": "ok"},
					}
					return Executed(executorResult), nil
				},
				ResultMaterializer: func(_ context.Context, _ ToolCallMeta, call *ToolCall, _ *planner.ToolResult) error {
					materialized = cloneToolCall(*call)
					return nil
				},
			},
		},
	}
	seedTestToolSpecs(rt, newAnyJSONSpec(toolName, toolset))
	input := &ToolInput{
		RunID:            "run-1",
		AgentID:          "svc.agent",
		ToolsetName:      toolset,
		ToolName:         toolName,
		ToolCallID:       "call-1",
		Payload:          rawjson.Message(`{"query":"status"}`),
		SessionID:        "session-1",
		Labels:           map[string]string{"scope": "canonical"},
		TurnID:           "turn-1",
		ParentToolCallID: "parent-1",
	}

	output, err := rt.ExecuteToolActivity(t.Context(), input)

	require.NoError(t, err)
	require.JSONEq(t, `{"status":"ok"}`, string(output.Payload))
	require.Equal(t, toolName, executorResult.Name)
	require.Equal(t, "call-1", executorResult.ToolCallID)
	require.Equal(t, toolName, materialized.Name)
	require.Equal(t, rawjson.Message(`{"query":"status"}`), materialized.Payload)
	require.Equal(t, agent.Ident("svc.agent"), materialized.AgentID)
	require.Equal(t, "run-1", materialized.RunID)
	require.Equal(t, "session-1", materialized.SessionID)
	require.Equal(t, map[string]string{"scope": "canonical"}, materialized.Labels)
	require.Equal(t, "turn-1", materialized.TurnID)
	require.Equal(t, "call-1", materialized.ToolCallID)
	require.Equal(t, "parent-1", materialized.ParentToolCallID)
	require.Equal(t, rawjson.Message(`{"query":"status"}`), input.Payload)
	require.Equal(t, map[string]string{"scope": "canonical"}, input.Labels)
}

func TestInlineToolExecutionIsolatesExecutorMutation(t *testing.T) {
	const (
		toolName = tools.Ident("svc.tools.lookup")
		toolset  = "svc.tools"
	)
	var executorResult *planner.ToolResult
	rt := &Runtime{
		Bus:           noopHooks{},
		RunEventStore: runloginmem.New(),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		toolsets: map[string]ToolsetRegistration{
			toolset: {
				Inline: true,
				Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					mutateEveryToolCallField(call)
					executorResult = &planner.ToolResult{
						Name:       call.Name,
						ToolCallID: call.ToolCallID,
						Result:     map[string]any{"status": "ok"},
					}
					return Executed(executorResult), nil
				},
			},
		},
	}
	seedTestToolSpecs(rt, newAnyJSONSpec(toolName, toolset))
	canonical := ToolCall{
		Name:                       toolName,
		Payload:                    rawjson.Message(`{"query":"status"}`),
		ModelName:                  "model.lookup",
		ModelPayload:               rawjson.Message(`{"query":"original"}`),
		AgentID:                    "svc.agent",
		RunID:                      "run-1",
		SessionID:                  "session-1",
		Labels:                     map[string]string{"scope": "canonical"},
		TurnID:                     "turn-1",
		ToolCallID:                 "call-1",
		ParentToolCallID:           "parent-1",
		ContinuationRootToolCallID: "root-1",
	}
	exec := &toolBatchExec{
		r:         rt,
		runID:     canonical.RunID,
		agentID:   canonical.AgentID,
		sessionID: canonical.SessionID,
		turnID:    canonical.TurnID,
		runCtx: &run.Context{
			RunID:     canonical.RunID,
			SessionID: canonical.SessionID,
			TurnID:    canonical.TurnID,
		},
	}

	batch, err := exec.dispatchToolCalls(&testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
	}, []ToolCall{canonical})

	require.NoError(t, err)
	require.Equal(t, canonical, batch.calls[0])
	outcome := batch.inlineByID[canonical.ToolCallID]
	require.NotNil(t, outcome)
	require.Equal(t, canonical.Name, outcome.ToolResult.Name)
	require.Equal(t, canonical.ToolCallID, outcome.ToolResult.ToolCallID)
	require.Equal(t, canonical.Name, executorResult.Name)
	require.Equal(t, canonical.ToolCallID, executorResult.ToolCallID)
	require.Equal(t, rawjson.Message(`{"query":"status"}`), canonical.Payload)
	require.Equal(t, rawjson.Message(`{"query":"original"}`), canonical.ModelPayload)
	require.Equal(t, map[string]string{"scope": "canonical"}, canonical.Labels)
}

func mutateEveryToolCallField(call *ToolCall) {
	call.Name = "mutated.tool"
	if len(call.Payload) > 0 {
		call.Payload[0] = '!'
	}
	call.ModelName = "mutated.model"
	if len(call.ModelPayload) > 0 {
		call.ModelPayload[0] = '!'
	}
	call.AgentID = "mutated.agent"
	call.RunID = "mutated-run"
	call.SessionID = "mutated-session"
	call.Labels["scope"] = "mutated"
	call.TurnID = "mutated-turn"
	call.ToolCallID = "mutated-call"
	call.ParentToolCallID = "mutated-parent"
	call.ContinuationRootToolCallID = "mutated-root"
}
