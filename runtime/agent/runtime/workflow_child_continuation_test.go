package runtime

// workflow_child_continuation_test.go verifies that a nested agent question
// suspends its parent and that the answer is delivered to a new child workflow
// before the parent tool call receives a result.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestChildSuspensionPropagatesThroughParentContinuation(t *testing.T) {
	runtime := New(WithLogger(telemetry.NoopLogger{}))
	parentRegistration := AgentRegistration{ResumeActivityName: "resume", ExecuteToolActivity: "execute"}
	runtime.agents["parent.agent"] = parentRegistration
	tool := newAnyJSONSpec("svc.agent.child")
	tool.IsAgentTool = true
	tool.AgentID = "nested.agent"
	cfg := AgentToolConfig{
		AgentID: "nested.agent",
		Name:    "svc.agent",
		Route: AgentRoute{
			ID: "nested.agent", WorkflowName: "nested.workflow", DefaultTaskQueue: "nested.queue",
		},
		AgentToolContent: AgentToolContent{Prompt: func(tools.Ident, any) string { return "work" }},
	}
	registration := NewAgentToolsetRegistration(runtime, cfg)
	runtime.toolsets[registration.Name] = registration
	seedTestToolset(runtime, registration.Name, tool)

	firstInput := &RunInput{AgentID: "parent.agent", RunID: "run-1", SessionID: "session-1", TurnID: "turn-1"}
	seedRunMeta(t, runtime, firstInput)
	firstChildren := make(chan *controlledChildHandle, 1)
	firstContext := &testWorkflowContext{
		ctx: t.Context(), hookRuntime: runtime, controlledChildHandles: firstChildren,
	}
	firstDone := make(chan struct {
		out *RunOutput
		err error
	}, 1)
	go func() {
		out, err := runtime.runLoop(
			firstContext,
			parentRegistration,
			firstInput,
			&planner.PlanInput{RunContext: run.Context{
				RunID: firstInput.RunID, SessionID: firstInput.SessionID, TurnID: firstInput.TurnID, Attempt: 1,
			}},
			&PlanResult{ToolCalls: []ToolCall{{
				Name: tool.Name, ToolCallID: "call-child", Payload: rawjson.Message(`{}`),
			}}},
			initialCaps(RunPolicy{MaxToolCalls: 1}),
			time.Time{}, time.Time{}, firstInput.TurnID, nil,
		)
		firstDone <- struct {
			out *RunOutput
			err error
		}{out: out, err: err}
	}()
	firstChild := <-firstChildren
	childRuntime := New(WithLogger(telemetry.NoopLogger{}))
	childTool := newAnyJSONSpec("child.lookup")
	seedTestToolSpecs(childRuntime, childTool)
	childSuspension := suspensionContractFixtureWithContext(
		t,
		childTool.Name,
		"nested.agent",
		firstContext.childRequests[0].Input.RunID,
		nil,
		nil,
	)
	firstChild.out = &api.RunOutput{
		AgentID:    "nested.agent",
		RunID:      firstContext.childRequests[0].Input.RunID,
		Suspension: childSuspension,
	}
	close(firstChild.ready)
	first := <-firstDone
	require.NoError(t, first.err)
	require.NotNil(t, first.out.Suspension)
	require.Equal(t, "clarification-1", first.out.Suspension.Pending[0].Await.Clarification.ID)
	parentEvents, err := runtime.ListRunEvents(t.Context(), firstInput.RunID, "", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countRunEventsByType(parentEvents, hooks.AwaitClarification))

	checkpoint, err := runtime.decodeWorkflowCheckpoint(first.out.Suspension)
	require.NoError(t, err)
	secondInput := &RunInput{
		AgentID: "parent.agent", RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.out.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID: "clarification-1", Answer: "Unit 7",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(secondInput, checkpoint))
	seedRunMeta(t, runtime, secondInput)
	secondChildren := make(chan *controlledChildHandle, 1)
	secondContext := &testWorkflowContext{
		ctx: t.Context(), hookRuntime: runtime, controlledChildHandles: secondChildren,
		hasPlanResult: true,
		planResult: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}},
		}}},
	}
	secondDone := make(chan struct {
		out *RunOutput
		err error
	}, 1)
	go func() {
		out, err := runtime.resumeSuspendedWorkflow(
			secondContext,
			parentRegistration,
			secondInput,
			checkpoint,
		)
		secondDone <- struct {
			out *RunOutput
			err error
		}{out: out, err: err}
	}()
	secondChild := <-secondChildren
	require.Equal(t, childSuspension.ID, secondContext.childRequests[0].Input.Continuation.Suspension.ID)
	require.Equal(t, "Unit 7", secondContext.childRequests[0].Input.Continuation.Response.Clarification.Answer)
	secondChild.out = &api.RunOutput{
		AgentID: "nested.agent", RunID: secondContext.childRequests[0].Input.RunID,
		Final: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "child done"}}},
	}
	close(secondChild.ready)
	second := <-secondDone
	require.NoError(t, second.err)
	require.NotNil(t, second.out)
	require.Nil(t, second.out.Suspension)
	require.Equal(t, "done", agentMessageText(second.out.Final))
	require.Equal(t, "run-1", secondContext.lastPlannerCall.Input.ToolOutputs[0].CallRunID)
	require.Equal(t, "run-2", secondContext.lastPlannerCall.Input.ToolOutputs[0].ResultRunID)
}
