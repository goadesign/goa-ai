package runtime

// workflow_child_continuation_test.go verifies that a nested agent question
// suspends its parent and that the answer is delivered to a new child workflow
// before the parent tool call receives a result.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

const continuationChildAgentID = "nested.agent"

func TestChildContinuationWaitsAfterParentCancellation(t *testing.T) {
	t.Parallel()

	runtime := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	tool := newAnyJSONSpec("svc.agent.child")
	tool.IsAgentTool = true
	tool.AgentID = continuationChildAgentID
	childTool := newAnyJSONSpec("child.lookup")
	cfg := AgentToolConfig{
		Definition:       testAgentDefinition(continuationChildAgentID, "nested.workflow", "nested.queue", []tools.ToolSpec{childTool}, nil),
		Name:             "svc.agent",
		AgentToolContent: AgentToolContent{Prompt: func(tools.Ident, any) string { return "work" }},
	}
	registration := NewAgentToolsetRegistration(cfg)
	runtime.toolsets[registration.Name] = registration
	seedTestToolset(runtime, registration.Name, tool)

	ctx, cancel := context.WithCancel(context.Background())
	childHandles := make(chan *controlledChildHandle, 1)
	wfCtx := &testWorkflowContext{
		ctx:                    ctx,
		hookRuntime:            runtime,
		controlledChildHandles: childHandles,
	}
	input := &RunInput{
		AgentID: "parent.agent", RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
	}
	base := &workflowConversation{RunContext: run.Context{
		RunID: input.RunID, SessionID: input.SessionID, TurnID: input.TurnID,
	}}
	suspension := suspensionContractFixtureWithContext(t, childTool.Name, continuationChildAgentID, "previous-child", nil, nil)
	admitRunForTest(t, runtime.Store, session.RunMeta{
		AgentID: continuationChildAgentID, RunID: "previous-child", SessionID: "session-1",
		Status: session.RunStatusRunning,
	})
	suspensionData, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), runtime.Store, "previous-child", session.RunSuspension{
		ID: suspension.ID, Data: suspensionData,
	}))
	batch := stepBatch{records: []stepToolRecord{{
		call: ToolCall{
			Name: tool.Name, ToolCallID: "call-child", Payload: rawjson.Message(`{}`),
		},
		childSuspension: suspension,
	}}}
	loop := &workflowLoop{r: runtime, wfCtx: wfCtx, input: input, base: base}
	pending := &checkpointChildContinuation{
		ToolCallID: "call-child",
		Suspension: suspension,
	}
	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
		ID: "clarification-1", Answer: "Unit 7",
	}}

	done := make(chan error, 1)
	go func() {
		_, err := loop.applyChildContinuation(&batch, pending, response)
		done <- err
	}()
	handle := waitForChildHandle(t, childHandles, "continued child")
	cancel()
	select {
	case err := <-done:
		t.Fatalf("parent continuation returned before the child finished cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	handle.err = context.Canceled
	close(handle.ready)
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestChildSuspensionPropagatesThroughParentContinuation(t *testing.T) {
	t.Run("compiled", func(t *testing.T) { testChildSuspensionContinuation(t, false) })
	t.Run("registry replacement and worker restart", func(t *testing.T) { testChildSuspensionContinuation(t, true) })
}

func testChildSuspensionContinuation(t *testing.T, dynamic bool) {
	runtime := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	tool := newAnyJSONSpec("svc.agent.child")
	tool.IsAgentTool = true
	tool.AgentID = continuationChildAgentID
	childTool := newAnyJSONSpec("child.lookup")
	childID := continuationChildAgentID
	if dynamic {
		childID = "generic.agent"
	}
	childDefinition := testAgentDefinition(agent.Ident(childID), "nested.workflow", "nested.queue", []tools.ToolSpec{childTool}, nil)
	parentDefinition := testAgentDefinitionWithChildren(
		"parent.agent", "parent.workflow", "parent.queue", []tools.ToolSpec{tool}, nil,
		[]AgentDefinition{childDefinition})

	call := ToolCall{Name: tool.Name, ToolCallID: "call-child", Payload: rawjson.Message(`{}`)}
	resolverCalls := 0
	if dynamic {
		call = testNativeRegistryCall(t, "revision/1")
		call.AgentID, call.RunID = "parent.agent", "run-1"
		parentDefinition = testAgentDefinition("parent.agent", "parent.workflow", "parent.queue", nil, nil).
			WithRegistryTools(testRegistrySources{}).WithAgentExecutors(&childDefinition)
		require.NoError(t, runtime.RegisterAgentToolResolver(childDefinition.route.ID, func(_ context.Context, revision string, _ *ToolCall) (*AgentToolConfiguration, error) {
			resolverCalls++
			require.Equal(t, "revision/1", revision)
			return &AgentToolConfiguration{Labels: map[string]string{"child_revision": revision}}, nil
		}))
	}
	parentRegistration := AgentRegistration{
		Definition: parentDefinition, ResumeActivityName: "resume", ExecuteToolActivity: "execute",
	}
	runtime.agents["parent.agent"] = parentRegistration
	cfg := AgentToolConfig{
		Definition:       childDefinition,
		Name:             "svc.agent",
		AgentToolContent: AgentToolContent{Prompt: func(tools.Ident, any) string { return "work" }},
	}
	registration := NewAgentToolsetRegistration(cfg)
	if !dynamic {
		runtime.toolsets[registration.Name] = registration
		seedTestToolset(runtime, registration.Name, tool)
	}

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
			&workflowConversation{RunContext: run.Context{
				RunID: firstInput.RunID, SessionID: firstInput.SessionID, TurnID: firstInput.TurnID, Attempt: 1,
			}},
			&PlanResult{ToolCalls: []ToolCall{call}},
			initialCaps(RunPolicy{MaxToolCalls: 1}),
			time.Time{}, time.Time{}, firstInput.TurnID, nil,
		)
		firstDone <- struct {
			out *RunOutput
			err error
		}{out: out, err: err}
	}()
	firstChild := waitForChildHandle(t, firstChildren, "first child")
	require.NotNil(t, firstChild)
	childRuntime := New(runtime.Store, WithLogger(telemetry.NoopLogger{}))
	childInput := firstContext.childRequests[0].Input
	seedRunMeta(t, childRuntime, childInput)
	seedTestToolSpecs(childRuntime, childTool)
	childSuspension := suspensionContractFixtureWithContext(
		t,
		childTool.Name,
		childID,
		firstContext.childRequests[0].Input.RunID,
		nil,
		nil,
	)
	if dynamic {
		rewriteSuspensionCheckpoint(t, childSuspension, func(checkpoint *workflowCheckpoint) {
			nested := agentChildRunContext(&call)
			nested.Labels = firstContext.childRequests[0].Input.Labels
			checkpoint.Context = checkpointContextFromRun(nested)
		})
	}
	suspensionData, err := json.Marshal(childSuspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), runtime.Store, childInput.RunID, session.RunSuspension{
		ID: childSuspension.ID, Data: suspensionData,
	}))
	firstChild.out = &api.RunOutput{
		AgentID:    childDefinition.route.ID,
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

	checkpoint, err := decodeWorkflowCheckpoint(first.out.Suspension, parentDefinition)
	require.NoError(t, err)
	if dynamic {
		require.Equal(t, 1, resolverCalls)
		// The new worker has no resolver or connection to the original catalog.
		// Only the checkpoint and preconfigured worker definitions survive.
		runtime = New(runtime.Store, WithLogger(telemetry.NoopLogger{}))
		runtime.agents["parent.agent"] = parentRegistration
		replacement := testNativeRegistryCall(t, "revision/2")
		require.NotEqual(t, string(call.Registry.Resolution), string(replacement.Registry.Resolution))
	}
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
	historyEndID := seedTestContinuationHistory(t, runtime, secondInput, checkpoint)
	go func() {
		out, err := runtime.resumeSuspendedWorkflow(
			secondContext,
			parentRegistration,
			secondInput,
			checkpoint, historyEndID,
		)
		secondDone <- struct {
			out *RunOutput
			err error
		}{out: out, err: err}
	}()
	var secondChild *controlledChildHandle
	select {
	case secondChild = <-secondChildren:
	case early := <-secondDone:
		t.Fatalf("continuation returned before starting its child: %v", early.err)
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not start its child within the fixture deadline")
	}
	require.Equal(t, childSuspension.ID, secondContext.childRequests[0].Input.Continuation.Suspension.ID)
	require.Equal(t, "Unit 7", secondContext.childRequests[0].Input.Continuation.Response.Clarification.Answer)
	require.NoError(t, validateWorkflowRunInput(secondContext.childRequests[0].Input))
	continuedCheckpoint, err := prepareContinuation(secondContext.childRequests[0].Input, childDefinition)
	require.NoError(t, err)
	if dynamic {
		require.Equal(t, "revision/1", continuedCheckpoint.Context.Labels["child_revision"])
		require.JSONEq(t, string(call.Registry.Resolution), string(continuedCheckpoint.Context.ToolRegistry.Resolution))
	}
	secondChild.out = &api.RunOutput{
		AgentID: childDefinition.route.ID, RunID: secondContext.childRequests[0].Input.RunID,
		Final: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "child done"}}},
	}
	if dynamic {
		secondChild.out.Final = nil
		secondChild.out.FinalToolResult = &api.ToolEvent{Name: call.Name, Result: rawjson.Message(`{"value":1}`)}
	}
	close(secondChild.ready)
	second := <-secondDone
	require.NoError(t, second.err)
	require.NotNil(t, second.out)
	require.Nil(t, second.out.Suspension)
	require.Equal(t, "done", second.out.Final.Text())
	require.Equal(t, "run-1", secondContext.lastPlannerCall.Input.ToolOutputs[0].CallRunID)
	require.Equal(t, "run-2", secondContext.lastPlannerCall.Input.ToolOutputs[0].ResultRunID)
}
