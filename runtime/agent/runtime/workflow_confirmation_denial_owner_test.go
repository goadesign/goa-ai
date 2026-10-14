package runtime

// Denied tool confirmations record the call and result in the continuation run,
// so planner history can read them without changing the suspended run.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestConfirmationDenialRecordsSuccessorHistory(t *testing.T) {
	tool := newAnyJSONSpec("svc.persist")
	executions := 0
	runtime := New(newTestStore(),
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
	registration := AgentRegistration{Definition: testRegistrationDefinition("agent-1", engine.WorkflowDefinition{}, []tools.ToolSpec{tool}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, ExecuteToolActivity: "execute"}
	agentID := registration.Definition.route.ID
	runtime.agents[agentID] = registration
	runtime.agentToolSpecs[agentID] = registration.Definition.specs

	firstInput := &RunInput{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))},

		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	recorder := &recordingHooks{}
	runtime.Bus = recorder
	seedRunMeta(t, runtime, firstInput)
	first, err := runtime.runLoop(
		&testWorkflowContext{ctx: t.Context(), runtime: runtime},
		registration,
		firstInput,
		&workflowConversation{finalization: &workflowFinalizationState{}, RunContext: run.Context{
			RunID: firstInput.RunID, SessionID: firstInput.SessionID, TurnID: firstInput.TurnID, Attempt: 1,
		}},
		&PlanResult{ToolCalls: []ToolCall{{
			Name: tool.Name, ToolCallID: "call-1", Payload: rawjson.Message(`{}`),
		}}},
		initialCaps(RunPolicy{MaxToolCalls: 8}),
		time.Time{}, time.Time{}, firstInput.TurnID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Zero(t, executions)

	checkpoint, err := decodeWorkflowCheckpoint(first.Suspension, registration.Definition)
	require.NoError(t, err)
	confirmation := first.Suspension.Pending[0].Confirmation
	secondInput := &RunInput{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.Suspension,
			Response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
				ID: confirmation.ID, Approved: false, RequestedBy: "operator",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(secondInput, checkpoint))
	seedRunMeta(t, runtime, secondInput)
	before, err := runtime.Store.ListRunRecords(t.Context(), firstInput.RunID, "", 100)
	require.NoError(t, err)
	require.Empty(t, before.NextCursor)
	predecessor, err := runtime.Store.LoadRun(t.Context(), firstInput.RunID)
	require.NoError(t, err)
	require.Equal(t, session.RunStatusSuspended, predecessor.Status)

	restored := runtime.restorePendingInputs(checkpoint.Pending, secondInput, &run.Context{RunID: secondInput.RunID, SessionID: secondInput.SessionID})
	require.Equal(t, firstInput.RunID, restored[0].Confirmation.Call.RunID)
	secondContext := &testWorkflowContext{
		ctx: t.Context(), hookRuntime: runtime, hasPlanResult: true,
		planResult: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "Persistence declined."}},
		}}},
	}
	registration.ResumeActivityName = "denial.resume"
	second, err := runtime.resumeSuspendedWorkflow(
		recoveryContinuationWorkflow(t, secondContext, checkpoint),
		registration,
		secondInput,
		checkpoint, seedTestContinuationHistory(t, runtime, secondInput, checkpoint),
	)

	require.NoError(t, err)
	require.NotNil(t, second)
	require.Nil(t, second.Suspension)
	require.Equal(t, "Persistence declined.", second.Final.Text())
	refs := secondContext.lastPlannerCall.Input.ToolOutputs
	require.Len(t, refs, 1)
	require.Equal(t, secondInput.RunID, refs[0].CallRunID)
	require.Equal(t, secondInput.RunID, refs[0].ResultRunID)
	outputs, err := runtime.loadPlannerToolOutputs(t.Context(), refs)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, rawjson.Message(`{"persisted":false}`), outputs[0].Result)
	messages := testActivityMessages(t, runtime, secondContext.lastPlannerCall.Input)
	require.NoError(t, transcript.ValidatePlannerTranscript(messages))
	require.NotEmpty(t, messages)
	var scheduled *hooks.ToolCallScheduledEvent
	var received *hooks.ToolResultReceivedEvent
	for _, event := range recorder.events {
		switch event := event.(type) {
		case *hooks.ToolCallScheduledEvent:
			scheduled = event
		case *hooks.ToolResultReceivedEvent:
			received = event
		}
	}
	require.NotNil(t, scheduled)
	require.NotNil(t, received)
	require.Equal(t, secondInput.RunID, scheduled.RunID())
	require.Equal(t, secondInput.RunID, received.RunID())
	require.Equal(t, secondInput.RunID, received.CallRunID)
	require.Equal(t, scheduled.ToolCallID, received.ToolCallID)
	after, err := runtime.Store.ListRunRecords(t.Context(), firstInput.RunID, "", 100)
	require.NoError(t, err)
	require.Equal(t, before, after)
	predecessorAfter, err := runtime.Store.LoadRun(t.Context(), firstInput.RunID)
	require.NoError(t, err)
	require.Equal(t, predecessor, predecessorAfter)
	require.Zero(t, executions)
}
