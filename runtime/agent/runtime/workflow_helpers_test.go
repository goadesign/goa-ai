package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func appendUserToolResultsForTest(t *testing.T, rt *Runtime, agentID agent.Ident, base *planner.PlanInput, calls []ToolCall, results []*planner.ToolResult) {
	t.Helper()
	records := stepToolRecordsForTest(t, calls, results)
	require.NoError(t, rt.appendUserToolRecordResults(t.Context(), agentID, base, records, ""))
}

func stepToolRecordsForTest(t *testing.T, calls []ToolCall, results []*planner.ToolResult) []stepToolRecord {
	t.Helper()
	records, err := stepToolRecordsFromCallsAndResults("test step tool records", calls, results)
	require.NoError(t, err)
	return records
}

// stepToolRecordsFromCallsAndResults pairs test fixtures by canonical tool-call
// identity so tests can exercise runtime record consumers directly.
func stepToolRecordsFromCallsAndResults(context string, calls []ToolCall, results []*planner.ToolResult) ([]stepToolRecord, error) {
	if len(calls) == 0 && len(results) == 0 {
		return nil, nil
	}
	if len(calls) != len(results) {
		return nil, fmt.Errorf("%s: calls/results length mismatch (%d != %d)", context, len(calls), len(results))
	}

	resultsByToolCallID := make(map[string]*planner.ToolResult, len(results))
	for _, result := range results {
		if result == nil {
			return nil, fmt.Errorf("%s: nil tool result", context)
		}
		if result.ToolCallID == "" {
			return nil, fmt.Errorf("%s: missing result tool_call_id for %s", context, result.Name)
		}
		if _, exists := resultsByToolCallID[result.ToolCallID]; exists {
			return nil, fmt.Errorf("%s: duplicate result tool_call_id %s", context, result.ToolCallID)
		}
		resultsByToolCallID[result.ToolCallID] = result
	}

	records := make([]stepToolRecord, 0, len(calls))
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, fmt.Errorf("%s: missing call tool_call_id for %s", context, call.Name)
		}
		result, ok := resultsByToolCallID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("%s: missing result for tool_call_id %s", context, call.ToolCallID)
		}
		if result.Name != "" && result.Name != call.Name {
			return nil, fmt.Errorf("%s: result name %s does not match call %s", context, result.Name, call.Name)
		}
		records = append(records, stepToolRecord{
			call:   call,
			result: result,
		})
	}
	return records, nil
}

func TestStepToolRecordsFromExecutionsRestoresCanonicalCallOrder(t *testing.T) {
	calls := []ToolCall{
		{Name: "svc.first", ToolCallID: "call-1"},
		{Name: "svc.second", ToolCallID: "call-2"},
		{Name: "svc.third", ToolCallID: "call-3"},
	}
	outcomes := []*ToolExecutionResult{
		{ToolResult: &planner.ToolResult{Name: "svc.first", ToolCallID: "call-1"}},
		{ToolResult: &planner.ToolResult{Name: "svc.third", ToolCallID: "call-3"}},
		{ToolResult: &planner.ToolResult{Name: "svc.second", ToolCallID: "call-2"}},
	}

	records, err := stepToolRecordsFromExecutions(calls, outcomes)
	require.NoError(t, err)
	require.Equal(t, "call-1", records[0].result.ToolCallID)
	require.Equal(t, "call-2", records[1].result.ToolCallID)
	require.Equal(t, "call-3", records[2].result.ToolCallID)
}

func TestCommitSelectedModelResponsePreservesCanonicalParts(t *testing.T) {
	rt := New(newTestStore())
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	transcript := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
			model.CitationsPart{Text: "answer", Citations: []model.Citation{{Title: "source"}}},
			model.ToolUsePart{
				ID:               "call-1",
				Name:             "svc.lookup",
				Input:            rawjson.Message(`{"q":"status"}`),
				ThoughtSignature: "tool-sig",
			},
		},
	}}

	require.NoError(t, rt.appendSelectedModelResponse(
		t.Context(),
		agentID,
		base,
		"turn-1",
		testPublicationBatchID,
		&PlanResult{},
		transcript,
	))

	require.Equal(t, transcript, base.Messages)
}

func TestCommitSelectedModelResponseBuildsPlannerAuthoredModelIdentity(t *testing.T) {
	rt := New(newTestStore())
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")
	result := &PlanResult{ToolCalls: []ToolCall{{
		Name:         "catalog.lookup.find_records",
		Payload:      rawjson.Message(`{"category":"recent"}`),
		ModelName:    "summarize_recent_records",
		ModelPayload: rawjson.Message(`{"period":"today"}`),
		ToolCallID:   "tooluse_1",
	}}}

	require.NoError(t, rt.appendSelectedModelResponse(
		t.Context(), agentID, base, "turn-1", testPublicationBatchID, result, nil,
	))

	require.Len(t, base.Messages, 1)
	require.Equal(t, []model.Part{model.ToolUsePart{
		ID:    "tooluse_1",
		Name:  "summarize_recent_records",
		Input: rawjson.Message(`{"period":"today"}`),
	}}, base.Messages[0].Parts)
}

func TestProviderToolCallIDCorrelatesTranscriptWhileExecutionIDOwnsRuntime(t *testing.T) {
	const (
		agentID            = agent.Ident("service.agent")
		runID              = "run-identity"
		sessionID          = "session-identity"
		turnID             = "turn-identity"
		providerToolCallID = "provider-call-1"
	)
	tool := newAnyJSONSpec("service.lookup")
	providerCall := model.ToolCall{
		ID:      providerToolCallID,
		Name:    tool.Name,
		Payload: rawjson.Message(`{"query":"status"}`),
	}
	var resumeInput *PlanActivityInput
	pl := &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			client, ok := input.Agent.ModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			require.NoError(t, err)
			require.Len(t, response.ToolCalls(), 1)
			request, err := planner.ToolRequestFromModelCall(response.ToolCalls()[0])
			require.NoError(t, err)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
		},
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.NoError(t, transcript.ValidatePlannerTranscript(input.Messages))
			require.Len(t, input.ToolOutputs, 1)
			require.Equal(t, providerToolCallID, input.ToolOutputs[0].ModelToolCallID)
			require.NotEqual(t, input.ToolOutputs[0].ToolCallID, input.ToolOutputs[0].ModelToolCallID)
			return finalPlannerResult("done"), nil
		},
	}
	rt := newTestRuntimeWithPlanner(agentID, pl)
	recorder := &recordingHooks{}
	rt.Bus = recorder
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "service",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{tool},
	}))
	rt.agentToolSpecs = make(map[agent.Ident][]tools.ToolSpec)
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{tool}
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse(nil, providerCall), nil
		},
	})
	reg := AgentRegistration{Definition: testRegistrationDefinition(agentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: pl,
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
		Policy:              RunPolicy{MaxToolCalls: 2},
	}
	rt.agents[agentID] = reg
	_, err := createSessionForTest(t.Context(), rt.Store, sessionID)
	require.NoError(t, err)
	var activityToolCallID string
	wfCtx := &routeWorkflowContext{
		ctx:         t.Context(),
		runID:       runID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": rt.PlanStartActivity,
			"resume": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				resumeInput = input
				return rt.PlanResumeActivity(ctx, input)
			},
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
				activityToolCallID = input.ToolCallID
				return rt.ExecuteToolActivity(ctx, input)
			},
		},
	}
	out, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   agentID,
		RunID:     runID,
		SessionID: sessionID,
		TurnID:    turnID,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Look up status."}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "done", out.Final.Text())

	executionToolCallID := generateDeterministicToolCallID(runID, turnID, 1, tool.Name, 0)
	require.NotEqual(t, providerToolCallID, executionToolCallID)
	require.Equal(t, executionToolCallID, activityToolCallID)
	require.NotNil(t, resumeInput)
	require.Len(t, resumeInput.ToolOutputs, 1)
	require.Equal(t, executionToolCallID, resumeInput.ToolOutputs[0].ToolCallID)
	require.NoError(t, transcript.ValidatePlannerTranscript(resumeInput.Messages))

	require.Len(t, resumeInput.Messages, 3)
	toolUse, ok := resumeInput.Messages[1].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	toolResult, ok := resumeInput.Messages[2].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, providerToolCallID, toolUse.ID)
	require.Equal(t, providerToolCallID, toolResult.ToolUseID)

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
	require.Equal(t, executionToolCallID, scheduled.ToolCallID)
	require.Equal(t, providerToolCallID, scheduled.ModelToolCallID)
	require.Equal(t, executionToolCallID, received.ToolCallID)
}

func TestAppendUserToolResults_IncludesErrorInToolResultContent(t *testing.T) {
	rt := New(newTestStore())
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := ToolCall{
		Name:       tools.Ident("svc.commands.update_record"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure:    testToolFailure(planner.FailureDomainRejection, planner.RecoveryFinish, "access denied: missing records.write privilege"),
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []ToolCall{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 1)

	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.True(t, part.IsError)
	require.Equal(t, call.ToolCallID, part.ToolUseID)
	require.Equal(t, "access denied: missing records.write privilege", part.Content)
}

func TestAppendUserToolResults_DecodesSuccessfulResultContent(t *testing.T) {
	rt := New(newTestStore())
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("svc.commands.update_record"),
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	})
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := ToolCall{
		Name:       tools.Ident("svc.commands.update_record"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result: map[string]any{
			"ok": false,
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []ToolCall{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 1)
	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.False(t, part.IsError)
	content, ok := part.Content.(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"ok": false}, content)
}

func TestAppendUserToolResults_MatchesReplayProjection(t *testing.T) {
	rt := New(newTestStore())
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("svc.commands.update_record"),
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	})
	agentID := agent.Ident("agent-1")
	call := ToolCall{
		Name:            tools.Ident("svc.commands.update_record"),
		ToolCallID:      "runtime-call-1",
		ModelToolCallID: "provider-call-1",
	}

	cases := []struct {
		name string
		tr   *planner.ToolResult
	}{
		{
			name: "success",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"ok": true,
				},
			},
		},
		{
			name: "error",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure:    testToolFailure(planner.FailureDomainRejection, planner.RecoveryFinish, "permission denied"),
			},
		},
		{
			name: "correction failure",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure:    testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "invalid query"),
			},
		},
		{
			name: "omitted",
			tr: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result: map[string]any{
					"blob": strings.Repeat("x", transcript.MaxToolResultContentBytes+1024),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}

			appendUserToolResultsForTest(t, rt, agentID, base, []ToolCall{call}, []*planner.ToolResult{tc.tr})
			require.Len(t, base.Messages, 1)

			livePart, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
			require.True(t, ok)
			require.Equal(t, call.ModelToolCallID, livePart.ToolUseID)

			resultJSON := ""
			if tc.tr.Result != nil {
				raw, err := rt.marshalToolValue(t.Context(), tc.tr.Name, tc.tr.Result, tc.tr.Bounds)
				require.NoError(t, err)
				resultJSON = string(raw)
			}
			errorMessage := ""
			if tc.tr.Failure != nil {
				errorMessage = tc.tr.Failure.Error.Error()
			}
			preview, err := formatToolResultPreviewForCall(t.Context(), rt, &call, tc.tr)
			require.NoError(t, err)
			replayContent, err := transcript.ProjectToolResultContent(
				rawjson.Message(resultJSON),
				tc.tr.Bounds,
				preview,
				errorMessage,
			)
			require.NoError(t, err)
			require.Equal(t, livePart.Content, replayContent)
		})
	}
}

func TestAppendUserToolResults_AppendsBoundsReminderAfterToolResults(t *testing.T) {
	rt := New(newTestStore())
	seedTestToolSpecs(rt, tools.ToolSpec{
		Name: tools.Ident("svc.read.list_devices"),
		Result: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				ToJSON: json.Marshal,
			},
		},
	})
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := ToolCall{
		Name:       tools.Ident("svc.read.list_devices"),
		ToolCallID: "tc-1",
	}
	cursor := "opaque-cursor"
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"devices": []any{}},
		Bounds: &agent.Bounds{
			Returned:   10,
			Total:      func() *int { v := 42; return &v }(),
			Truncated:  true,
			NextCursor: &cursor,
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []ToolCall{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleSystem, base.Messages[1].Role)

	txt, ok := base.Messages[1].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Contains(t, txt.Text, "A tool call returned a bounded/truncated result.")
	require.Contains(t, txt.Text, "Next cursor: opaque-cursor")
}

func TestAppendUserToolResults_UsesContinuationActionNameInBoundsReminder(t *testing.T) {
	search, continuation := continuationTestSpecs()

	tests := []struct {
		name string
		call ToolCall
	}{
		{
			name: "source page",
			call: ToolCall{
				Name:       search.Name,
				ToolCallID: "source-call",
			},
		},
		{
			name: "continued page",
			call: ToolCall{
				Name:                       continuation.Name,
				ToolCallID:                 "page-call",
				ContinuationRootToolCallID: "source-call",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := New(newTestStore())
			seedTestToolSpecs(rt, search, continuation)
			base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
			cursor := "opaque-cursor"
			result := &planner.ToolResult{
				Name:       test.call.Name,
				ToolCallID: test.call.ToolCallID,
				Result:     map[string]any{"items": []any{"result"}},
				Bounds: &agent.Bounds{
					Returned:   1,
					Truncated:  true,
					NextCursor: &cursor,
				},
			}

			appendUserToolResultsForTest(
				t,
				rt,
				"agent-1",
				base,
				[]ToolCall{test.call},
				[]*planner.ToolResult{result},
			)

			reminder := base.Messages[1].Parts[0].(model.TextPart).Text
			want := continuationActionName(continuation.Name, "source-call").String()
			require.Contains(t, reminder, "call "+want)
			require.NotContains(t, reminder, "call tools_continue_search")
			require.NotContains(t, reminder, "page-call")
		})
	}
}

func TestAppendUserToolResults_OmitsBoundsReminderForStandalonePlannerContinuation(t *testing.T) {
	_, continuation := continuationTestSpecs()
	continuation.ResultReminder = "Review the returned records before finishing."
	rt := New(newTestStore())
	seedTestToolSpecs(rt, continuation)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	cursor := "private-next-page"
	call := ToolCall{
		Name:       continuation.Name,
		ToolCallID: "page-call",
	}
	result := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"items": []any{"result"}},
		Bounds: &agent.Bounds{
			Returned:   1,
			Truncated:  true,
			NextCursor: &cursor,
		},
	}

	appendUserToolResultsForTest(
		t,
		rt,
		"agent-1",
		base,
		[]ToolCall{call},
		[]*planner.ToolResult{result},
	)

	require.Len(t, base.Messages, 2)
	reminder := base.Messages[1].Parts[0].(model.TextPart).Text
	require.Contains(t, reminder, continuation.ResultReminder)
	require.NotContains(t, reminder, "bounded/truncated")
	require.NotContains(t, reminder, cursor)
	require.NotContains(t, reminder, continuationToolNamePrefix)
}

func TestWorkflowTreatsPlannerAuthoredCanonicalContinuationAsStandalone(t *testing.T) {
	const (
		agentID   = agent.Ident("service.agent")
		sessionID = "session-standalone-continuation"
		runID     = "run-standalone-continuation"
		turnID    = "turn-standalone-continuation"
	)
	search, continuation := continuationTestSpecs()
	continuation.ResultReminder = "Review the returned records before finishing."
	cursor := "private-next-page"
	var resumes int
	plannerImpl := &stubPlanner{
		start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			require.Empty(t, input.Agent.AdvertisedToolDefinitions())
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    continuation.Name,
				Payload: rawjson.Message(`{"cursor":"first"}`),
			}}}, nil
		},
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			require.Empty(t, input.Agent.AdvertisedToolDefinitions())
			require.Len(t, input.ToolOutputs, 1)
			require.Empty(t, input.ToolOutputs[0].ModelToolCallID)
			require.Empty(t, input.ToolOutputs[0].ContinuationRootToolCallID)
			require.NotNil(t, input.ToolOutputs[0].Bounds)
			require.NotNil(t, input.ToolOutputs[0].Bounds.NextCursor)
			require.Equal(t, cursor, *input.ToolOutputs[0].Bounds.NextCursor)
			var transcriptText strings.Builder
			var visibleToolResults int
			for _, message := range input.Messages {
				for _, part := range message.Parts {
					if text, ok := part.(model.TextPart); ok {
						transcriptText.WriteString(text.Text)
					}
					if result, ok := part.(model.ToolResultPart); ok {
						visibleToolResults++
						content, err := json.Marshal(result.Content)
						require.NoError(t, err)
						require.NotContains(t, string(content), cursor)
					}
				}
			}
			require.Equal(t, 1, visibleToolResults)
			require.Contains(t, transcriptText.String(), continuation.ResultReminder)
			require.NotContains(t, transcriptText.String(), "bounded/truncated")
			require.NotContains(t, transcriptText.String(), cursor)
			require.NotContains(t, transcriptText.String(), continuationToolNamePrefix)
			return finalPlannerResult("done"), nil
		},
	}
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tools",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			require.Equal(t, continuation.Name, call.Name)
			require.Empty(t, call.ModelToolCallID)
			require.Empty(t, call.ContinuationRootToolCallID)
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"items": []any{"result"}},
				Bounds: &agent.Bounds{
					Returned:   1,
					Truncated:  true,
					NextCursor: &cursor,
				},
			}, nil
		}),
		Specs: []tools.ToolSpec{search, continuation},
	}))
	definition := NewAgentDefinition(
		AgentRoute{
			ID:               agentID,
			WorkflowName:     "service.agent.workflow",
			DefaultTaskQueue: "test",
		},
		[]tools.ToolSpec{search, continuation},
		nil,
		nil,
		[]tools.Ident{continuation.Name},
		nil,
	)
	rt.agents[agentID] = AgentRegistration{
		Definition:          definition,
		Planner:             plannerImpl,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
		Policy:              RunPolicy{MaxToolCalls: 2},
	}
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{continuation}
	_, err := createSessionForTest(t.Context(), rt.Store, sessionID)
	require.NoError(t, err)
	wfCtx := &routeWorkflowContext{
		ctx:         t.Context(),
		runID:       runID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan":   rt.PlanStartActivity,
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}

	output, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   agentID,
		RunID:     runID,
		SessionID: sessionID,
		TurnID:    turnID,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Continue the prior alarm query."}},
		}},
	})

	require.NoError(t, err)
	require.NotNil(t, output)
	require.Equal(t, "done", output.Final.Text())
	require.Equal(t, 1, resumes)
	require.Len(t, output.ToolEvents, 1)
}

func TestAppendUserToolResults_UsesRefinementWithoutContinuationCursor(t *testing.T) {
	search, continuation := continuationTestSpecs()
	rt := New(newTestStore())
	seedTestToolSpecs(rt, search, continuation)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	call := ToolCall{
		Name:       search.Name,
		ToolCallID: "source-call",
	}
	result := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"items": []any{"result"}},
		Bounds: &agent.Bounds{
			Returned:       1,
			Truncated:      true,
			RefinementHint: "Narrow the time window.",
		},
	}

	appendUserToolResultsForTest(
		t,
		rt,
		"agent-1",
		base,
		[]ToolCall{call},
		[]*planner.ToolResult{result},
	)

	reminder := base.Messages[1].Parts[0].(model.TextPart).Text
	require.Contains(t, reminder, "Refinement hint: Narrow the time window.")
	require.NotContains(t, reminder, "More matching results are available.")
	require.NotContains(t, reminder, "call continue_")
}

func TestRecoveryReminderIsEphemeralPlannerInput(t *testing.T) {
	rt := New(newTestStore())
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := ToolCall{
		Name:       tools.Ident("svc.read.aggregate"),
		ToolCallID: "tc-1",
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInvalidCall,
			Error: planner.NewToolError("Unsupported filter field."),
			Recovery: planner.RecoveryDirective{
				Action:      planner.RecoveryCorrectCall,
				PriorInput:  rawjson.Message(`{"bad":"field"}`),
				ExampleJSON: rawjson.Message(`{"dataset":"alarms"}`),
			},
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, []ToolCall{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)

	reminders := rt.recoveryReminders([]*planner.ToolOutput{{
		Name:       tr.Name,
		ToolCallID: tr.ToolCallID,
		Failure:    tr.Failure,
	}})
	require.Len(t, reminders, 1)
	require.Contains(t, reminders[0].Text, "A tool call failed.")
	require.Contains(t, reminders[0].Text, "Tool: svc_read_aggregate")
	require.Contains(t, reminders[0].Text, "failed tool remains available with correction guidance")
}

func TestAppendUserToolResultsPreservesBookkeepingResults(t *testing.T) {
	rt := New(newTestStore())
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.tools.read"),
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("workflow.progress.set_step_status")
			spec.Bookkeeping = true
			return spec
		}(),
	)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	calls := []ToolCall{
		{Name: "svc.tools.read", ToolCallID: "call-1"},
		{Name: "workflow.progress.set_step_status", ToolCallID: "call-2"},
	}
	results := []*planner.ToolResult{
		{
			Name:       "svc.tools.read",
			ToolCallID: "call-1",
			Result:     map[string]any{"value": 1},
		},
		{
			Name:       "workflow.progress.set_step_status",
			ToolCallID: "call-2",
			Result:     map[string]any{"ok": true},
		},
	}

	appendUserToolResultsForTest(t, rt, agentID, base, calls, results)
	require.Len(t, base.Messages, 1)
	require.Equal(t, model.ConversationRoleUser, base.Messages[0].Role)
	require.Len(t, base.Messages[0].Parts, 2)

	part, ok := base.Messages[0].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-1", part.ToolUseID)
	bookkeeping, ok := base.Messages[0].Parts[1].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-2", bookkeeping.ToolUseID)
}

func TestRecoveryRemindersDescribeSelectedTransition(t *testing.T) {
	rt := New(newTestStore())
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("svc.tools.correct"),
		newAnyJSONSpec("svc.tools.finish"),
	)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	calls := []ToolCall{
		{Name: "svc.tools.correct", ToolCallID: "call-1", Payload: rawjson.Message(`{"bad":true}`)},
		{Name: "svc.tools.finish", ToolCallID: "call-2", Payload: rawjson.Message(`{}`)},
	}
	results := []*planner.ToolResult{
		{
			Name:       calls[0].Name,
			ToolCallID: calls[0].ToolCallID,
			Failure:    testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "correct me"),
		},
		{
			Name:       calls[1].Name,
			ToolCallID: calls[1].ToolCallID,
			Failure:    testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "stop now"),
		},
	}

	appendUserToolResultsForTest(t, rt, "agent-1", base, calls, results)

	require.Len(t, base.Messages, 1)
	outputs := []*planner.ToolOutput{
		{Name: results[0].Name, ToolCallID: results[0].ToolCallID, Failure: results[0].Failure},
		{Name: results[1].Name, ToolCallID: results[1].ToolCallID, Failure: results[1].Failure},
	}
	finishReminders := rt.recoveryReminders(outputs[1:])
	require.Len(t, finishReminders, 1)
	require.Contains(t, finishReminders[0].Text, "Do not retry this failed tool.")
	require.Contains(t, finishReminders[0].Text, "advertised continuation")
	require.NotContains(t, finishReminders[0].Text, "remains available")

	correctionReminders := rt.recoveryReminders(outputs[:1])
	require.Len(t, correctionReminders, 1)
	require.Contains(t, correctionReminders[0].Text, "failed tool remains available")
}

func TestAppendUserToolResults_ReplaysRetryableBookkeepingFailures(t *testing.T) {
	rt := New(newTestStore())
	seedTestToolSpecs(
		rt,
		func() tools.ToolSpec {
			spec := newAnyJSONSpec("workflow.progress.complete")
			spec.Bookkeeping = true
			spec.TerminalRun = true
			return spec
		}(),
	)
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1"}}
	agentID := agent.Ident("agent-1")

	call := ToolCall{
		Name:       "workflow.progress.complete",
		ToolCallID: "call-1",
		Payload:    rawjson.Message(`{"title":"Final report"}`),
	}
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure:    testToolFailure(planner.FailureInvalidCall, planner.RecoveryReplan, "report.summary length must be <= 600"),
	}

	require.NoError(t, rt.appendSelectedModelResponse(
		t.Context(),
		agentID,
		base,
		"",
		testPublicationBatchID,
		&PlanResult{ToolCalls: []ToolCall{call}},
		[]*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:               "call-1",
				Name:             string(call.Name),
				Input:            call.Payload,
				ThoughtSignature: "opaque-provider-signature",
			}},
		}},
	))
	appendUserToolResultsForTest(t, rt, agentID, base, []ToolCall{call}, []*planner.ToolResult{tr})

	require.Len(t, base.Messages, 2)
	require.Equal(t, model.ConversationRoleAssistant, base.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, base.Messages[1].Role)

	usePart, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", usePart.ID)
	require.Equal(t, "opaque-provider-signature", usePart.ThoughtSignature)

	resultPart, ok := base.Messages[1].Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, "call-1", resultPart.ToolUseID)
	require.True(t, resultPart.IsError)
}
