package runtime

// workflow_confirmation_continuation_test.go proves that tool confirmation
// ends one workflow and executes only after a typed decision starts another.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestConfirmationExecutesInContinuationWorkflow(t *testing.T) {
	tool := newAnyJSONSpec("svc.update")
	tool.Bookkeeping = true
	tool.TerminalRun = true
	executions := 0
	runtime := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
			tool.Name: {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "Apply the update?", nil
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]any{"updated": false}, nil
				},
			},
		}}),
	)
	require.NoError(t, runtime.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"updated": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{tool},
	}))

	firstInput := &RunInput{AgentID: "agent-1", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
	seedRunMeta(t, runtime, firstInput)
	firstContext := &testWorkflowContext{ctx: t.Context(), runtime: runtime}
	first, err := runtime.runLoop(
		firstContext,
		AgentRegistration{ExecuteToolActivity: "execute"},
		firstInput,
		&planner.PlanInput{RunContext: run.Context{
			RunID: firstInput.RunID, SessionID: firstInput.SessionID, TurnID: firstInput.TurnID, Attempt: 1,
		}},
		&PlanResult{ToolCalls: []ToolCall{{
			Name: tool.Name, ToolCallID: "call-1", Payload: rawjson.Message(`{}`),
		}}},
		initialCaps(RunPolicy{MaxToolCalls: 1}),
		time.Time{}, time.Time{}, firstInput.TurnID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Zero(t, executions)

	checkpoint, err := runtime.decodeWorkflowCheckpoint(first.Suspension)
	require.NoError(t, err)
	confirmation := first.Suspension.Pending[0].Confirmation
	secondInput := &RunInput{
		AgentID: "agent-1", RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.Suspension,
			Response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
				ID: confirmation.ID, Approved: true, RequestedBy: "operator",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(secondInput, checkpoint))
	seedRunMeta(t, runtime, secondInput)
	secondContext := &testWorkflowContext{ctx: t.Context(), runtime: runtime}
	second, err := runtime.resumeSuspendedWorkflow(
		secondContext,
		AgentRegistration{ExecuteToolActivity: "execute"},
		secondInput,
		checkpoint,
	)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Nil(t, second.Suspension)
	require.Equal(t, 1, executions)
	require.Len(t, second.ToolEvents, 1)
}

func TestCompletionToolConfirmationDenialFailsContinuation(t *testing.T) {
	tool := newAnyJSONSpec("svc.persist")
	executions := 0
	runtime := New(
		WithLogger(telemetry.NoopLogger{}),
		WithToolConfirmation(&ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{
			tool.Name: {
				Prompt: func(context.Context, *ToolCall) (string, error) {
					return "Persist the result?", nil
				},
				DeniedResult: func(context.Context, *ToolCall) (any, error) {
					return map[string]any{"persisted": false}, nil
				},
			},
		}}),
	)
	require.NoError(t, runtime.RegisterToolset(ToolsetRegistration{
		Name: "svc",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"persisted": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{tool},
	}))
	registration := AgentRegistration{
		ID:                  "agent-1",
		ExecuteToolActivity: "execute",
		Specs:               []tools.ToolSpec{tool},
	}
	runtime.agents[registration.ID] = registration
	runtime.agentToolSpecs[registration.ID] = registration.Specs

	firstInput := &RunInput{
		AgentID:   registration.ID,
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Policy:    &PolicyOverrides{CompletionTool: tool.Name},
	}
	seedRunMeta(t, runtime, firstInput)
	first, err := runtime.runLoop(
		&testWorkflowContext{ctx: t.Context(), runtime: runtime},
		registration,
		firstInput,
		&planner.PlanInput{RunContext: run.Context{
			RunID: firstInput.RunID, SessionID: firstInput.SessionID, TurnID: firstInput.TurnID, Attempt: 1,
		}},
		&PlanResult{ToolCalls: []ToolCall{{
			Name: tool.Name, ToolCallID: "call-1", Payload: rawjson.Message(`{}`),
		}}},
		initialCaps(RunPolicy{MaxToolCalls: 1}),
		time.Time{}, time.Time{}, firstInput.TurnID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Zero(t, executions)

	checkpoint, err := runtime.decodeWorkflowCheckpoint(first.Suspension)
	require.NoError(t, err)
	confirmation := first.Suspension.Pending[0].Confirmation
	secondInput := &RunInput{
		AgentID: registration.ID, RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.Suspension,
			Response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
				ID: confirmation.ID, Approved: false, RequestedBy: "operator",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(secondInput, checkpoint))
	seedRunMeta(t, runtime, secondInput)

	second, err := runtime.resumeSuspendedWorkflow(
		&testWorkflowContext{ctx: t.Context(), runtime: runtime},
		registration,
		secondInput,
		checkpoint,
	)

	require.Nil(t, second)
	require.ErrorContains(t, err, `completion tool "svc.persist" did not succeed`)
	require.ErrorContains(t, err, "execution was denied by confirmation")
	require.Zero(t, executions)
}
