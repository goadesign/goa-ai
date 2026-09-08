package runtime

// Completion statistics include results restored from the current suspension
// checkpoint, not just tools executed by the continuation workflow.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompletionStatisticsIncludeContinuationHistory(t *testing.T) {
	page, final := newAnyJSONSpec("records.page"), newAnyJSONSpec("records.finish")
	final.TerminalRun, final.Bookkeeping = true, true
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}), WithToolConfirmation(&ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{final.Name: {
			Prompt:       func(context.Context, *ToolCall) (string, error) { return "Finish?", nil },
			DeniedResult: func(context.Context, *ToolCall) (any, error) { return map[string]any{"done": false}, nil },
		}},
	}))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "records", Specs: []tools.ToolSpec{page, final},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID,
				Result: map[string]any{"done": true}, Telemetry: &telemetry.ToolTelemetry{TokensUsed: 7, DurationMs: 11, Model: "model"}}, nil
		}),
	}))
	reg := AgentRegistration{
		Definition:       NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, []tools.ToolSpec{page, final}, nil, nil, []tools.Ident{page.Name, final.Name}, nil),
		WorkflowHandler:  rt.ExecuteWorkflow,
		PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.tool",
		Planner: &stubPlanner{
			start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: page.Name, Payload: rawjson.Message(`{}`)}}}, nil
			},
			resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: final.Name, Payload: rawjson.Message(`{}`)}}}, nil
			},
		},
	}
	require.NoError(t, rt.RegisterAgent(t.Context(), reg))
	_, err := createSessionForTest(t.Context(), rt.Store, "completion-session")
	require.NoError(t, err)
	firstInput := &api.RunInput{AgentID: "records.agent", RunID: "first", SessionID: "completion-session", TurnID: "turn"}
	firstHandle, err := rt.Engine.StartWorkflow(t.Context(), engine.WorkflowStartRequest{ID: "first", Workflow: "records.workflow", TaskQueue: "records.queue", Input: firstInput})
	require.NoError(t, err)
	first, err := firstHandle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	assert.Equal(t, "goa-ai.run-suspension.v8", first.Suspension.Version)
	assert.Contains(t, string(first.Suspension.Checkpoint), `"ToolEvents":[`)
	assert.NotContains(t, string(first.Suspension.Checkpoint), `"ToolCount"`)
	assert.Equal(t, 1, first.ToolCount)
	assert.Equal(t, 7, first.ToolTelemetry.TokensUsed)
	checkpoint, err := decodeWorkflowCheckpoint(first.Suspension, reg.Definition)
	require.NoError(t, err)
	require.Len(t, checkpoint.State.ToolEvents, 1)
	assert.Equal(t, page.Name, checkpoint.State.ToolEvents[0].Name)

	secondInput := &api.RunInput{AgentID: "records.agent", RunID: "second", SessionID: "completion-session", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{Suspension: first.Suspension, Response: &api.PendingInputResponse{
			Confirmation: &api.ConfirmationDecision{ID: first.Suspension.Pending[0].Confirmation.ID, Approved: true, RequestedBy: "operator"},
		}},
	}
	secondHandle, err := rt.Engine.StartWorkflow(t.Context(), engine.WorkflowStartRequest{ID: "second", Workflow: "records.workflow", TaskQueue: "records.queue", Input: secondInput})
	require.NoError(t, err)
	second, err := secondHandle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, second.FinalToolResult)
	assert.Equal(t, final.Name, second.FinalToolResult.Name)
	assert.Equal(t, 2, second.ToolCount)
	assert.Equal(t, &telemetry.ToolTelemetry{TokensUsed: 14, DurationMs: 22, Model: "model"}, second.ToolTelemetry)
	assert.Equal(t, 7, second.FinalToolResult.Telemetry.TokensUsed)
	assert.Len(t, storedToolResults(t, rt, "first"), 1)
	assert.Len(t, storedToolResults(t, rt, "second"), 1)
}
