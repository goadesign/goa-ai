package runtime

// planner_events_test.go tests transparent model invocation matching and
// tool-call provenance independently from model-client wrapping.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestRuntimePlannerEventsExportsNoTranscriptWithoutModelInvocation(t *testing.T) {
	e := &modelInvocationJournal{}
	transcript, err := e.exportModelInvocation(&planner.PlanResult{})

	require.NoError(t, err)
	require.Nil(t, transcript)
}

func TestRuntimePlannerEventsMatchesEarlierInvocationByExactToolCall(t *testing.T) {
	e := &modelInvocationJournal{}
	first := e.beginModelInvocation()
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
	second := e.beginModelInvocation()
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
			ToolCallID: "call-1",
			Name:       "svc.lookup",
			Payload:    []byte(`{"query":"first"}`),
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
	e.beginModelInvocation()

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		}},
	})

	require.NoError(t, err)
	require.Nil(t, transcript)
}

func TestRuntimePlannerEventsMatchesStreamedFinalResponse(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
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
		Streamed:      true,
	})

	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true}, transcript[0].Parts[0])
}

func TestRuntimePlannerEventsMatchesCompleteFinalResponse(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
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

func TestRuntimePlannerEventsRejectsFinalResponseThatDiscardsToolCalls(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
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
	invocation := e.beginModelInvocation()
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

func TestRuntimePlannerEventsPreservesCanonicalResponseForModifiedPresentation(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "streamed"}[streamed], func(t *testing.T) {
			e := &modelInvocationJournal{}
			invocation := e.beginModelInvocation()
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
				Streamed:      streamed,
			})

			require.NoError(t, err)
			require.Equal(t, "original", transcript[0].Parts[0].(model.TextPart).Text)
		})
	}
}

func TestRuntimePlannerEventsRejectsCallsMixedAcrossInvocations(t *testing.T) {
	e := &modelInvocationJournal{}
	first := e.beginModelInvocation()
	second := e.beginModelInvocation()
	mustRecordModelResponse(t, e, first, testModelResponse(nil, model.ToolCall{
		ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`),
	}))
	mustRecordModelResponse(t, e, second, testModelResponse(nil, model.ToolCall{
		ID: "call-2", Name: "svc.lookup", Payload: []byte(`{}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{ToolCallID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)},
			{ToolCallID: "call-2", Name: "svc.lookup", Payload: []byte(`{}`)},
		},
	})

	require.EqualError(t, err, "planner result modified or mixed model-authored tool calls")
}

func TestRuntimePlannerEventsMatchesAwaitCallTransparently(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "question-1",
		Name:    "chat.ask_question",
		Payload: []byte(`{"title":"Choose"}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		Await: planner.NewAwait(planner.AwaitQuestionsItem(&planner.AwaitQuestions{
			ToolCallID: "question-1",
			ToolName:   "chat.ask_question",
			Payload:    []byte(`{"title":"Choose"}`),
		})),
	})

	require.NoError(t, err)
}

func TestRuntimePlannerEventsRejectsModifiedModelToolCall(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "call-1",
		Name:    "svc.lookup",
		Payload: []byte(`{"query":"original"}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ToolCallID: "call-1",
			Name:       "svc.lookup",
			Payload:    []byte(`{"query":"modified"}`),
		}},
	})

	require.EqualError(t, err, "planner result modified or mixed model-authored tool calls")
}

func TestRuntimePlannerEventsPreservesProviderOrderWhenPlannerGroupsCalls(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil,
		model.ToolCall{ID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		model.ToolCall{ID: "call-2", Name: "svc.second", Payload: []byte(`{}`)},
	))

	transcript, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{ToolCallID: "call-2", Name: "svc.second", Payload: []byte(`{}`)},
			{ToolCallID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "call-1", transcript[0].Parts[0].(model.ToolUsePart).ID)
}

func TestRuntimePlannerEventsRejectsDuplicatePlannerToolCallIdentity(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil,
		model.ToolCall{ID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		model.ToolCall{ID: "call-2", Name: "svc.second", Payload: []byte(`{}`)},
	))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{ToolCallID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
			{ToolCallID: "call-1", Name: "svc.first", Payload: []byte(`{}`)},
		},
	})

	require.EqualError(t, err, "planner result modified or mixed model-authored tool calls")
}

func TestRuntimePlannerEventsMatchesCompiledToolByModelIdentity(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
	mustRecordModelResponse(t, e, invocation, testModelResponse(nil, model.ToolCall{
		ID:      "call-1",
		Name:    "planner.resolve",
		Payload: []byte(`{"scope":"all"}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			ToolCallID:   "call-1",
			Name:         "service.execute",
			Payload:      []byte(`{"compiled":true}`),
			ModelName:    "planner.resolve",
			ModelPayload: []byte(`{"scope":"all"}`),
		}},
	})

	require.NoError(t, err)
}

func TestRuntimePlannerEventsRejectsAmbiguousInvocation(t *testing.T) {
	e := &modelInvocationJournal{}
	first := e.beginModelInvocation()
	second := e.beginModelInvocation()
	response := testModelResponse(nil, model.ToolCall{
		ID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`),
	})
	mustRecordModelResponse(t, e, first, response)
	mustRecordModelResponse(t, e, second, response)

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{ToolCallID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)}},
	})

	require.EqualError(t, err, "planner result matches multiple model invocations")
}

func TestRuntimePlannerEventsCanonicalResponseReplacesStreamDeltas(t *testing.T) {
	e := &modelInvocationJournal{}
	invocation := e.beginModelInvocation()
	require.NoError(t, e.recordModelChunk(invocation, model.TextChunk{
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
		ToolCalls: []planner.ToolRequest{{ToolCallID: "call-1", Name: "svc.lookup", Payload: []byte(`{}`)}},
	})

	require.NoError(t, err)
	require.Equal(t, "canonical", agentMessageText(transcript[0]))
	require.Equal(t, map[string]any{"provider_item": "item-1"}, transcript[0].Meta)
}

func TestRuntimePlannerEventsIgnoresIncompleteAndInvalidAttempts(t *testing.T) {
	e := &modelInvocationJournal{}
	incomplete := e.beginModelInvocation()
	require.NoError(t, e.recordModelChunk(incomplete, model.ToolCallChunk{
		ToolCall: model.ToolCall{ID: "incomplete", Name: "svc.lookup", Payload: []byte(`{}`)},
	}))
	invalid := e.beginModelInvocation()
	require.EqualError(t, e.recordModelResponse(invalid, testModelResponse(nil,
		model.ToolCall{ID: "duplicate", Name: "svc.lookup", Payload: []byte(`{}`)},
		model.ToolCall{ID: "duplicate", Name: "svc.lookup", Payload: []byte(`{}`)},
	)), `model: response content 0 part 1: duplicate tool call ID "duplicate"`)
	accepted := e.beginModelInvocation()
	mustRecordModelResponse(t, e, accepted, testModelResponse(nil, model.ToolCall{
		ID: "accepted", Name: "svc.lookup", Payload: []byte(`{}`),
	}))

	_, err := e.exportModelInvocation(&planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{ToolCallID: "accepted", Name: "svc.lookup", Payload: []byte(`{}`)}},
	})

	require.NoError(t, err)
}

func mustRecordModelResponse(
	t *testing.T,
	events *modelInvocationJournal,
	invocationID modelInvocationID,
	response *model.Response,
) {
	t.Helper()
	require.NoError(t, events.recordModelResponse(invocationID, response))
}
