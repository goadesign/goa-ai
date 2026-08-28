package runtime

// planner_events_test.go checks how saved model responses are matched to the
// exact planner result that selected them.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRuntimePlannerEventsExportsNoTranscriptWithoutModelInvocation(t *testing.T) {
	e := &modelInvocationJournal{}
	transcript, err := e.exportModelInvocation(&planner.PlanResult{})

	require.NoError(t, err)
	require.Nil(t, transcript)
}

func TestRuntimePlannerEventsMatchesEarlierInvocationByExactToolCall(t *testing.T) {
	e := &modelInvocationJournal{}
	first := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, first, testModelResponse([]model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "first"}},
	}},
		model.ToolCall{
			ID:               "call-1",
			Name:             "svc.lookup",
			Payload:          []byte(`{"query":"first"}`),
			ThoughtSignature: "sig-1",
		},
	))
	second := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, second, testModelResponse(nil,
		model.ToolCall{
			ID:               "call-2",
			Name:             "svc.lookup",
			Payload:          []byte(`{"query":"second"}`),
			ThoughtSignature: "sig-2",
		},
	))

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ModelToolCallID: "call-1",
			Name:            "svc.lookup",
			Payload:         []byte(`{"query":"first"}`),
		}},
	})
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, model.ConversationRoleAssistant, transcript[0].Role)
	require.Equal(t, []model.Part{
		model.TextPart{Text: "first"},
		model.ToolUsePart{
			ID:               "call-1",
			Name:             "svc.lookup",
			Input:            rawjson.Message(`{"query":"first"}`),
			ThoughtSignature: "sig-1",
		},
	}, transcript[0].Parts)
}

func TestRuntimePlannerEventsTerminalResultNeedsNoInvocationReplay(t *testing.T) {
	e := &modelInvocationJournal{}
	mustBeginModelInvocation(t, e)

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		}},
	})

	require.NoError(t, err)
	require.Nil(t, transcript)
}

func TestRuntimePlannerEventsMatchesCompleteFinalResponse(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
				model.TextPart{Text: "done"},
			},
		}},
		StopReason: "end_turn",
	}
	mustRecordModelResponse(t, e, invocation, response)

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	})

	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true}, transcript[0].Parts[0])
}

func TestRuntimePlannerEventsRequestsReplacementForOutputLimitedFinalResponse(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "partial"}},
		}},
		StopReason:    "max_tokens",
		OutputLimited: true,
	}
	mustRecordModelResponse(t, e, invocation, response)
	result := &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	}

	transcript, err := e.exportModelInvocation(result)

	require.Nil(t, transcript)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Equal(t, outputLimitCorrection, outputErr.Correction())
	require.Same(t, result.FinalResponse.Message, outputErr.ModelMessage())
	require.Equal(t, planner.OutputContractOriginModel, outputErr.Origin())
}

func TestRuntimePlannerEventsRejectsOutputLimitedToolBatch(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := testModelResponse(nil, model.ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{}`),
	})
	response.StopReason = "max_tokens"
	response.OutputLimited = true
	mustRecordModelResponse(t, e, invocation, response)

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ModelToolCallID: "call-1",
			Name:            "svc.lookup",
			Payload:         []byte(`{}`),
		}},
	})

	require.Nil(t, transcript)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.Empty(t, outputErr.Correction())
	require.Equal(t, planner.OutputContractOriginModel, outputErr.Origin())
}

func TestRuntimePlannerEventsRejectsFinalResponseThatDiscardsToolCalls(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := testModelResponse(nil, model.ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: rawjson.Message(`{"query":"status"}`),
	})
	mustRecordModelResponse(t, e, invocation, response)

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[0]},
	})

	require.ErrorContains(t, err, "did not preserve the selected model invocation")
	require.Nil(t, transcript)
}

func TestRuntimePlannerEventsMatchesAllAssistantResponseContent(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := &model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "first "}},
			},
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "second"}},
			},
		},
		StopReason: "end_turn",
	}
	mustRecordModelResponse(t, e, invocation, response)

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[1]},
	})

	require.NoError(t, err)
	require.Len(t, transcript, 2)
}

func TestRuntimePlannerEventsDistinguishesIdenticalMessagesByOrigin(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := &model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "same"}},
			},
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "same"}},
			},
		},
		StopReason: "end_turn",
	}
	mustRecordModelResponse(t, e, invocation, response)
	result := &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &response.Content[1]},
	}

	transcript, err := e.exportModelInvocation(result)

	require.NoError(t, err)
	require.Len(t, transcript, 2)
	require.False(t, model.SameMessageOrigin(transcript[0], result.FinalResponse.Message))
	require.True(t, model.SameMessageOrigin(transcript[1], result.FinalResponse.Message))
}

func TestRuntimePlannerEventsRejectsModifiedProviderContent(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	response := &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "original"}},
		}},
		StopReason: "end_turn",
	}
	mustRecordModelResponse(t, e, invocation, response)
	presentation := &response.Content[0]
	presentation.Parts = []model.Part{model.TextPart{Text: "modified"}}

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: presentation},
	})

	require.EqualError(t, err, "planner result modified provider-owned message content")
	require.Nil(t, transcript)
}

func TestRuntimePlannerEventsRejectsCallsMixedAcrossInvocations(t *testing.T) {
	e := &modelInvocationJournal{}
	first := mustBeginModelInvocation(t, e)
	second := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, first, testModelResponse(nil, model.ToolCall{
		ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`),
	}))
	mustRecordModelResponse(t, e, second, testModelResponse(nil, model.ToolCall{
		ID: "call-2", Name: "svc.lookup", Payload: []byte(`{}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{ModelToolCallID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)},
			{ModelToolCallID: "call-2", Name: "svc.lookup", Payload: []byte(`{}`)},
		},
	})

	require.EqualError(t, err, "planner result modified or mixed model-authored tool calls")
}

func TestRuntimePlannerEventsMatchesAwaitCallTransparently(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "question-1",
		Name:    "assistant.ask_question",
		Payload: []byte(`{"title":"Choose"}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		Await: planner.NewAwait(planner.AwaitQuestionsItem(&planner.AwaitQuestions{
			ModelToolCallID: "question-1",
			ToolName:        "assistant.ask_question",
			Payload:         []byte(`{"title":"Choose"}`),
		})),
	})

	require.NoError(t, err)
}

func TestRuntimePlannerEventsMatchesToolClarificationCallTransparently(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "clarification-1",
		Name:    "assistant.ask_clarification",
		Payload: []byte(`{"question":"Which device?"}`),
	}))

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		Await: planner.NewAwait(planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
			ModelToolCallID: "clarification-1",
			ToolName:        "assistant.ask_clarification",
			Payload:         []byte(`{"question":"Which device?"}`),
			Question:        "Which device?",
		})),
	})

	require.NoError(t, err)
	require.Len(t, transcript, 1)
	toolUse, ok := transcript[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "clarification-1", toolUse.ID)
}

func TestRuntimePlannerEventsRecordsOriginalPayloadForCompiledToolCall(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{"query":"original"}`),
	}))

	result := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ModelToolCallID: "call-1",
			Name:            "svc.lookup",
			Payload:         []byte(`{"query":"modified"}`),
		}},
	}
	_, err := e.exportModelInvocation(result)

	require.NoError(t, err)
	bindings, err := e.selectedCompiledModelCalls(result)
	require.NoError(t, err)
	calls, err := (&Runtime{}).compilePlannerToolCalls(result.ToolCalls, nil, bindings)
	require.NoError(t, err)
	require.Equal(t, tools.Ident("svc.lookup"), calls[0].ModelName)
	require.JSONEq(t, `{"query":"original"}`, string(calls[0].ModelPayload))
}

func TestRuntimePlannerEventsPreservesProviderOrderWhenPlannerGroupsCalls(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil,
		model.ToolCall{ID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		model.ToolCall{ID: "call-2", Name: "svc.second", Payload: []byte(`{}`)},
	))

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{ModelToolCallID: "call-2", Name: "svc.second", Payload: []byte(`{}`)},
			{ModelToolCallID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "call-1", transcript[0].Parts[0].(model.ToolUsePart).ID)
}

func TestRuntimePlannerEventsRejectsDuplicatePlannerToolCallIdentity(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil,
		model.ToolCall{ID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		model.ToolCall{ID: "call-2", Name: "svc.second", Payload: []byte(`{}`)},
	))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{ModelToolCallID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
			{ModelToolCallID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		},
	})

	require.EqualError(t, err, "planner result modified or mixed model-authored tool calls")
}

func TestRuntimePlannerEventsMatchesCompiledToolByModelIdentity(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "call-1",
		Name:    "planner.resolve",
		Payload: []byte(`{"scope":"all"}`),
	}))

	result := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ModelToolCallID: "call-1",
			Name:            "service.execute",
			Payload:         []byte(`{"compiled":true}`),
		}},
	}
	transcript, err := e.exportModelInvocation(result)

	require.NoError(t, err)
	bindings, err := e.selectedCompiledModelCalls(result)
	require.NoError(t, err)
	calls, err := (&Runtime{}).compilePlannerToolCalls(result.ToolCalls, nil, bindings)
	require.NoError(t, err)
	require.Equal(t, tools.Ident("planner.resolve"), calls[0].ModelName)
	require.JSONEq(t, `{"scope":"all"}`, string(calls[0].ModelPayload))
	require.Len(t, transcript, 1)
	require.Len(t, transcript[0].Parts, 1)
	toolUse, ok := transcript[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", toolUse.ID)
	require.Equal(t, "planner.resolve", toolUse.Name)
	require.JSONEq(t, `{"scope":"all"}`, string(toolUse.Input))
}

func TestRuntimePlannerEventsRejectsAmbiguousInvocation(t *testing.T) {
	e := &modelInvocationJournal{}
	first := mustBeginModelInvocation(t, e)
	second := mustBeginModelInvocation(t, e)
	response := testModelResponse(nil, model.ToolCall{
		ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`),
	})
	mustRecordModelResponse(t, e, first, response)
	mustRecordModelResponse(t, e, second, response)

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{ModelToolCallID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)}},
	})

	require.EqualError(t, err, "planner result matches multiple model invocations")
}

func TestRuntimePlannerEventsCanonicalResponseReplacesStreamDeltas(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := mustBeginModelInvocation(t, e)
	require.NoError(t, e.recordModelChunk(t.Context(), invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "partial"}},
		},
	}))
	mustRecordModelResponse(t, e, invocation, testModelResponse([]model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "canonical"}},
		Meta:  map[string]any{"provider_item": "item-1"},
	}},
		model.ToolCall{
			ID:      "call-1",
			Name:    "svc.lookup",
			Payload: []byte(`{}`),
		},
	))

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{ModelToolCallID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)}},
	})

	require.NoError(t, err)
	require.Equal(t, "canonical", agentMessageText(transcript[0]))
	require.Equal(t, map[string]any{"provider_item": "item-1"}, transcript[0].Meta)
}

func TestRuntimePlannerEventsRejectsCallsAfterMalformedResponse(t *testing.T) {
	e := &modelInvocationJournal{}
	incomplete := mustBeginModelInvocation(t, e)
	require.NoError(t, e.recordModelChunk(t.Context(), incomplete, model.ToolCallChunk{
		ToolCall: model.ToolCall{ID: "incomplete", Name: "svc.lookup", Payload: []byte(`{}`)},
	}))
	invalid := mustBeginModelInvocation(t, e)
	response := testModelResponse(nil,
		model.ToolCall{ID: "duplicate", Name: "svc.lookup", Payload: []byte(`{}`)},
		model.ToolCall{ID: "duplicate", Name: "svc.lookup", Payload: []byte(`{}`)},
	)
	validationErr := model.ValidateResponse(response)
	require.Error(t, validationErr)
	rejectedErr := outputcontract.NewWithOrigin(
		validationErr,
		planner.OutputContractOriginModel,
	)
	err := e.stageRejectedModelOutput(invalid, model.ResponseEvidence{Present: true}, rejectedErr)
	require.NoError(t, err)
	require.NoError(t, e.finalizeModelInvocation(invalid, modelcall.Outcome{
		ProviderCall: modelcall.Result{Called: true},
		Validations:  []modelcall.Result{{Called: true, Err: validationErr}},
		CompletionObservers: []modelcall.Result{{
			Called: true,
			Err:    rejectedErr,
		}},
	}))
	_, err = e.beginModelInvocation("", func() {})
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	require.ErrorContains(t, err, `duplicate tool call ID "duplicate"`)
}

func mustBeginModelInvocation(t *testing.T, events *modelInvocationJournal) modelInvocationID {
	t.Helper()
	invocationID, err := events.beginModelInvocation("", func() {})
	require.NoError(t, err)
	return invocationID
}

func mustRecordModelResponse(
	t *testing.T,
	events *modelInvocationJournal,
	invocationID modelInvocationID,
	response *model.Response,
) {
	t.Helper()
	calls := response.ToolCalls()
	toolNames := make([]string, 0, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		name := string(call.Name)
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		toolNames = append(toolNames, name)
	}
	contract, err := model.NewRequestContract(testModelRequest(toolNames...))
	require.NoError(t, err)
	owned, err := contract.ValidateResponse(response)
	require.NoError(t, err)
	*response = *owned
	require.NoError(t, events.recordValidatedModelResponse(invocationID, response))
}
