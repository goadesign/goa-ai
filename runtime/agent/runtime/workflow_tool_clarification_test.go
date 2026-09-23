package runtime

// workflow_tool_clarification_test.go verifies that free-text, structured
// question, and external-tool responses complete the exact model-authored calls
// that initiated each external-input request.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/eval/evidence"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestRunLoopToolClarificationPreservesCallAndReturnsAnswer(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	events := &recordingHooks{}
	rt.Bus = events
	tool := newAnyJSONSpec(tools.Ident("assistant.ask_clarification"))
	seedTestToolSpecs(rt, tool)

	wfCtx := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt}

	base := &workflowConversation{RunContext: run.Context{
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Attempt:   1,
	}}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &PlanResult{Await: planner.NewAwait(
		planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
			ID:              "clarification-await-1",
			ToolName:        tool.Name,
			ModelToolCallID: "provider-clarification-call-1",
			Payload:         rawjson.Message(`{"question":"Which record group and time window?"}`),
			Question:        "Which record group and time window?",
		}),
	)}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Empty(t, wfCtx.lastPlannerCall.Name)
	require.Len(t, out.Suspension.Pending, 1)
	require.Equal(t, api.PendingInputKindClarification, out.Suspension.Pending[0].Kind)
	await := out.Suspension.Pending[0].Await.ToolClarification
	require.NotNil(t, await)
	runtimeToolCallID := generateDeterministicToolCallID("run-1", "turn-1", 1, tool.Name, 0)
	require.Equal(t, runtimeToolCallID, await.ToolCallID)
	require.Equal(t, "provider-clarification-call-1", await.ModelToolCallID)
	require.NotEqual(t, await.ToolCallID, await.ModelToolCallID)

	// Project the actual run-loop schedule and await hooks through the public
	// Subscriber. The lifecycle fixtures model the workflow wrapper, which is
	// outside this focused runLoop test.
	firstSink := &recordingStreamSink{}
	firstSubscriber, err := stream.NewSubscriber(firstSink, stream.RuntimeHostProfile())
	require.NoError(t, err)
	for _, event := range events.events {
		require.NoError(t, firstSubscriber.HandleEvent(t.Context(), event))
	}
	require.NoError(t, firstSubscriber.HandleEvent(t.Context(), hooks.NewRunSuspendedEvent(
		"run-1", input.AgentID, input.SessionID, "suspension-1", "v1", 1, nil,
	)))
	firstCollector := evidence.NewCollector()
	sawAwait := false
	for _, event := range firstSink.snapshot() {
		require.NoError(t, firstCollector.Consume(event))
		sawAwait = sawAwait || event.Type() == stream.EventAwaitClarification
	}
	require.True(t, sawAwait)
	require.True(t, firstCollector.Done())
	firstEvidence, err := firstCollector.Finish()
	require.NoError(t, err)
	require.Len(t, firstEvidence.ToolCalls, 1)
	require.Equal(t, runtimeToolCallID, firstEvidence.ToolCalls[0].ToolCallID)
	require.False(t, firstEvidence.ToolCalls[0].Completed)
	firstEventCount := len(events.events)

	checkpoint, err := decodeWorkflowCheckpoint(out.Suspension, testRuntimeDefinition(rt, "svc.agent"))
	require.NoError(t, err)
	continuedCtx := &testWorkflowContext{
		ctx:           t.Context(),
		hookRuntime:   rt,
		planResult:    &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}
	continuedInput := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-2",
		SessionID: "session-1",
		TurnID:    "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: out.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID:     "clarification-await-1",
				Answer: "Use record_group_1 over the past 24 hours.",
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(continuedInput, checkpoint))
	out, err = rt.resumeSuspendedWorkflow(
		continuedCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		continuedInput,
		checkpoint, seedTestContinuationHistory(t, rt, continuedInput, checkpoint),
	)
	require.NoError(t, err)
	require.Equal(t, "done", out.Final.Text())
	require.Equal(t, "resume", continuedCtx.lastPlannerCall.Name)
	require.NoError(t, transcript.ValidatePlannerTranscript(testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)))
	require.Len(t, testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input), 2)
	require.Len(t, continuedCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "run-1", continuedCtx.lastPlannerCall.Input.ToolOutputs[0].CallRunID)
	require.Equal(t, "run-2", continuedCtx.lastPlannerCall.Input.ToolOutputs[0].ResultRunID)
	var hookResult *hooks.ToolResultReceivedEvent
	for _, event := range events.events {
		if candidate, ok := event.(*hooks.ToolResultReceivedEvent); ok {
			hookResult = candidate
		}
	}
	require.NotNil(t, hookResult)
	require.Equal(t, "run-1", hookResult.CallRunID)
	require.Equal(t, "run-2", hookResult.RunID())
	require.Equal(t, runtimeToolCallID, hookResult.ToolCallID)

	secondSink := &recordingStreamSink{}
	secondSubscriber, err := stream.NewSubscriber(secondSink, stream.RuntimeHostProfile())
	require.NoError(t, err)
	require.NoError(t, secondSubscriber.HandleEvent(t.Context(), hooks.NewRunPhaseChangedEvent(
		"run-2", continuedInput.AgentID, continuedInput.SessionID, run.PhasePlanning,
	)))
	for _, event := range events.events[firstEventCount:] {
		require.NoError(t, secondSubscriber.HandleEvent(t.Context(), event))
	}
	require.NoError(t, secondSubscriber.HandleEvent(t.Context(), hooks.NewRunCompletedEvent(
		"run-2", continuedInput.AgentID, continuedInput.SessionID, "success", run.PhaseCompleted, nil, nil, nil,
	)))
	secondCollector, err := evidence.NewContinuationCollector(firstEvidence, "run-2")
	require.NoError(t, err)
	for _, event := range secondSink.snapshot() {
		require.NoError(t, secondCollector.Consume(event))
	}
	require.True(t, secondCollector.Done())
	secondEvidence, err := secondCollector.Finish()
	require.NoError(t, err)
	require.Empty(t, secondEvidence.ToolCalls)
	require.Len(t, secondEvidence.ToolCompletions, 1)
	require.Equal(t, "run-1", secondEvidence.ToolCompletions[0].InvocationRootRunID)
	require.Equal(t, runtimeToolCallID, secondEvidence.ToolCompletions[0].Call.ToolCallID)
	require.JSONEq(t, `{"answer":"Use record_group_1 over the past 24 hours."}`,
		string(secondEvidence.ToolCompletions[0].Call.Result))

	assistant := testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)[0]
	require.Equal(t, model.ConversationRoleAssistant, assistant.Role)
	toolUse, ok := assistant.Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "provider-clarification-call-1", toolUse.ID)

	user := testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)[1]
	require.Equal(t, model.ConversationRoleUser, user.Role)
	toolResult, ok := user.Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, toolUse.ID, toolResult.ToolUseID)
	require.Equal(t, map[string]any{
		"answer": "Use record_group_1 over the past 24 hours.",
	}, toolResult.Content)
}

func TestRunLoopQuestionsPreservesProviderAndRuntimeIdentityAcrossResume(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	events := &recordingHooks{}
	rt.Bus = events
	tool := newAnyJSONSpec(tools.Ident("assistant.ask_question"))
	seedTestToolSpecs(rt, tool)

	base := &workflowConversation{RunContext: run.Context{
		RunID: "run-1", SessionID: "session-1", TurnID: "turn-1", Attempt: 1,
	}}
	input := &RunInput{
		AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "session-1", TurnID: "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &PlanResult{Await: planner.NewAwait(planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID:              "questions-await-1",
		ToolName:        tool.Name,
		ModelToolCallID: "provider-question-call-1",
		Payload:         rawjson.Message(`{"title":"Choose a record group"}`),
		Questions:       []planner.AwaitQuestion{{ID: "record_group", Prompt: "Which record group?"}},
	}))}

	out, err := rt.runLoop(
		&testWorkflowContext{ctx: t.Context(), hookRuntime: rt},
		AgentRegistration{ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out.Suspension)
	require.Len(t, out.Suspension.Pending, 1)
	await := out.Suspension.Pending[0].Await.Questions
	require.NotNil(t, await)
	runtimeToolCallID := generateDeterministicToolCallID("run-1", "turn-1", 1, tool.Name, 0)
	require.Equal(t, runtimeToolCallID, await.ToolCallID)
	require.Equal(t, "provider-question-call-1", await.ModelToolCallID)
	require.NotEqual(t, await.ToolCallID, await.ModelToolCallID)

	checkpoint, err := decodeWorkflowCheckpoint(out.Suspension, testRuntimeDefinition(rt, "svc.agent"))
	require.NoError(t, err)
	continuedCtx := &testWorkflowContext{
		ctx:         t.Context(),
		hookRuntime: rt,
		planResult: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		}}},
		hasPlanResult: true,
	}
	continuedInput := &RunInput{
		AgentID: agent.Ident("agent-1"), RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: out.Suspension,
			Response: &api.PendingInputResponse{ToolResults: &api.ToolResultsSet{
				ID: "questions-await-1",
				Results: []*api.ProvidedToolResult{{
					Name:       tool.Name,
					ToolCallID: runtimeToolCallID,
					Success:    &api.ProvidedToolSuccess{Result: rawjson.Message(`{"answer":"record_group_1"}`)},
				}},
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(continuedInput, checkpoint))
	out, err = rt.resumeSuspendedWorkflow(
		continuedCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		continuedInput,
		checkpoint, seedTestContinuationHistory(t, rt, continuedInput, checkpoint),
	)
	require.NoError(t, err)
	require.Equal(t, "done", out.Final.Text())
	require.Len(t, continuedCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, runtimeToolCallID, continuedCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
	require.NoError(t, transcript.ValidatePlannerTranscript(testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)))
	require.Len(t, testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input), 2)
	toolUse := testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)[0].Parts[0].(model.ToolUsePart)
	toolResult := testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)[1].Parts[0].(model.ToolResultPart)
	require.Equal(t, "provider-question-call-1", toolUse.ID)
	require.Equal(t, tool.Name.String(), toolUse.Name)
	require.JSONEq(t, `{"title":"Choose a record group"}`, string(toolUse.Input))
	require.Equal(t, "provider-question-call-1", toolResult.ToolUseID)

	var resultEvent *hooks.ToolResultReceivedEvent
	for _, event := range events.events {
		if candidate, ok := event.(*hooks.ToolResultReceivedEvent); ok {
			resultEvent = candidate
		}
	}
	require.NotNil(t, resultEvent)
	require.Equal(t, runtimeToolCallID, resultEvent.ToolCallID)
}

func TestRunLoopExternalToolsPreservesIdentityForSuccessAndCorrection(t *testing.T) {
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	events := &recordingHooks{}
	rt.Bus = events
	firstTool := newAnyJSONSpec(tools.Ident("external.read_first"))
	secondTool := newAnyJSONSpec(tools.Ident("external.read_second"))
	seedTestToolSpecs(rt, firstTool, secondTool)

	base := &workflowConversation{RunContext: run.Context{
		RunID: "run-1", SessionID: "session-1", TurnID: "turn-1", Attempt: 1,
	}}
	input := &RunInput{
		AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "session-1", TurnID: "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &PlanResult{Await: planner.NewAwait(planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
		ID: "external-await-1",
		Items: []planner.AwaitToolItem{
			{
				Name:            firstTool.Name,
				ModelToolCallID: "provider-external-call-1",
				Payload:         rawjson.Message(`{"query":"first"}`),
			},
			{
				Name:            secondTool.Name,
				ModelToolCallID: "provider-external-call-2",
				Payload:         rawjson.Message(`{"query":"second"}`),
			},
		},
	}))}

	out, err := rt.runLoop(
		&testWorkflowContext{ctx: t.Context(), hookRuntime: rt},
		AgentRegistration{ResumeActivityName: "resume"},
		input,
		base,
		initial,
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out.Suspension)
	require.Len(t, out.Suspension.Pending, 1)
	await := out.Suspension.Pending[0].Await.ExternalTools
	require.NotNil(t, await)
	require.Len(t, await.Items, 2)
	firstRuntimeID := generateDeterministicToolCallID("run-1", "turn-1", 1, firstTool.Name, 0)
	secondRuntimeID := generateDeterministicToolCallID("run-1", "turn-1", 1, secondTool.Name, 1)
	require.Equal(t, firstRuntimeID, await.Items[0].ToolCallID)
	require.Equal(t, secondRuntimeID, await.Items[1].ToolCallID)
	require.NotEqual(t, firstRuntimeID, secondRuntimeID)
	require.Equal(t, "provider-external-call-1", await.Items[0].ModelToolCallID)
	require.Equal(t, "provider-external-call-2", await.Items[1].ModelToolCallID)

	checkpoint, err := decodeWorkflowCheckpoint(out.Suspension, testRuntimeDefinition(rt, "svc.agent"))
	require.NoError(t, err)
	continuedCtx := &testWorkflowContext{
		ctx:         t.Context(),
		hookRuntime: rt,
		recoveryCatalog: &api.RecoveryCatalog{
			Tools: []tools.Ident{secondTool.Name},
		},
		planResult: &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		}}},
		hasPlanResult: true,
	}
	continuedInput := &RunInput{
		AgentID: agent.Ident("agent-1"), RunID: "run-2", SessionID: "session-1", TurnID: "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: out.Suspension,
			Response: &api.PendingInputResponse{ToolResults: &api.ToolResultsSet{
				ID: "external-await-1",
				Results: []*api.ProvidedToolResult{
					{
						Name:       firstTool.Name,
						ToolCallID: firstRuntimeID,
						Success:    &api.ProvidedToolSuccess{Result: rawjson.Message(`{"value":"first"}`)},
					},
					{
						Name:       secondTool.Name,
						ToolCallID: secondRuntimeID,
						Failure: &api.ProvidedToolFailure{
							Kind:    planner.FailureInvalidCall,
							Message: "second query is invalid",
							Action:  planner.RecoveryCorrectCall,
						},
					},
				},
			}},
		},
	}
	require.NoError(t, restoreContinuationRunInput(continuedInput, checkpoint))
	out, err = rt.resumeSuspendedWorkflow(
		continuedCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		continuedInput,
		checkpoint, seedTestContinuationHistory(t, rt, continuedInput, checkpoint),
	)
	require.NoError(t, err)
	require.Equal(t, "done", out.Final.Text())
	outputs := continuedCtx.lastPlannerCall.Input.ToolOutputs
	require.Len(t, outputs, 2)
	require.Equal(t, firstRuntimeID, outputs[0].ToolCallID)
	require.Equal(t, secondRuntimeID, outputs[1].ToolCallID)
	require.Equal(t, []string{secondRuntimeID}, continuedCtx.lastPlannerCall.Input.RecoveryToolCallIDs)

	require.NoError(t, transcript.ValidatePlannerTranscript(testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)))
	require.Len(t, testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input), 2)
	assistant := testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)[0]
	user := testActivityMessages(t, rt, continuedCtx.lastPlannerCall.Input)[1]
	require.Len(t, assistant.Parts, 2)
	require.Len(t, user.Parts, 2)
	firstUse := assistant.Parts[0].(model.ToolUsePart)
	secondUse := assistant.Parts[1].(model.ToolUsePart)
	require.Equal(t, "provider-external-call-1", firstUse.ID)
	require.Equal(t, firstTool.Name.String(), firstUse.Name)
	require.JSONEq(t, `{"query":"first"}`, string(firstUse.Input))
	require.Equal(t, "provider-external-call-2", secondUse.ID)
	require.Equal(t, secondTool.Name.String(), secondUse.Name)
	require.JSONEq(t, `{"query":"second"}`, string(secondUse.Input))
	require.Equal(t, "provider-external-call-1", user.Parts[0].(model.ToolResultPart).ToolUseID)
	require.Equal(t, "provider-external-call-2", user.Parts[1].(model.ToolResultPart).ToolUseID)

	resultEvents := make(map[string]*hooks.ToolResultReceivedEvent)
	for _, event := range events.events {
		if candidate, ok := event.(*hooks.ToolResultReceivedEvent); ok {
			resultEvents[candidate.ToolCallID] = candidate
		}
	}
	require.Contains(t, resultEvents, firstRuntimeID)
	require.Contains(t, resultEvents, secondRuntimeID)
	require.Nil(t, resultEvents[firstRuntimeID].Failure)
	require.NotNil(t, resultEvents[secondRuntimeID].Failure)
	require.Equal(t, planner.RecoveryCorrectCall, resultEvents[secondRuntimeID].Failure.Recovery.Action)
	require.JSONEq(t, `{"query":"second"}`, string(resultEvents[secondRuntimeID].Failure.Recovery.PriorInput))
}

func TestRunLoopSessionlessRunRejectsExternalInput(t *testing.T) {
	runtime := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	input := &RunInput{AgentID: "agent-1", RunID: "run-1", TurnID: "turn-1"}
	base := &workflowConversation{RunContext: run.Context{
		RunID: "run-1", TurnID: "turn-1", Attempt: 1,
	}}
	result := &PlanResult{Await: planner.NewAwait(
		planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarification-1", Question: "Which unit?",
		}),
	)}

	_, err := runtime.runLoop(
		&testWorkflowContext{ctx: t.Context()},
		AgentRegistration{},
		input,
		base,
		result,
		initialCaps(RunPolicy{}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.ErrorContains(t, err, "sessionless run cannot request external input")
}
