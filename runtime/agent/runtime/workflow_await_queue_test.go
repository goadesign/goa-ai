package runtime

// workflow_await_queue_test.go verifies await publication does not duplicate
// the selected provider response committed by the workflow step.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestPublishAwaitToolUsesQuestionsDoesNotDuplicateCommittedResponse(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("assistant.ask_question"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1", SessionID: "sess-1"}}
	input := &RunInput{AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "sess-1"}
	item := planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID:              "await-1",
		ToolName:        "assistant.ask_question",
		ToolCallID:      "runtime-call-1",
		ModelToolCallID: "provider-call-1",
		Payload:         rawjson.Message(`{}`),
		Questions:       []planner.AwaitQuestion{{ID: "q1", Prompt: "which?"}},
	})
	result := &planner.PlanResult{Await: planner.NewAwait(item)}
	transcript := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.ToolUsePart{
			ID:               "provider-call-1",
			Name:             "assistant.ask_question",
			Input:            rawjson.Message(`{}`),
			ThoughtSignature: "opaque-provider-signature",
		}},
	}}

	require.NoError(t, rt.appendSelectedModelResponse(
		t.Context(), input.AgentID, base, "turn-1", &PlanResult{Await: result.Await}, transcript,
	))
	require.NoError(t, rt.publishAwaitToolUses(t.Context(), input, base, "turn-1", item, 0))

	require.Len(t, base.Messages, 1)
	require.Len(t, base.Messages[0].Parts, 1)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "provider-call-1", use.ID)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
}

func TestPublishAwaitToolUsesExternalToolsDoesNotRecordAssistantResponse(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.a"), newAnyJSONSpec("svc.tools.b"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1", SessionID: "sess-1"}}
	input := &RunInput{AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "sess-1"}
	item := planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
		ID: "await-1",
		Items: []planner.AwaitToolItem{
			{
				Name:            "svc.tools.a",
				ToolCallID:      "runtime-call-1",
				ModelToolCallID: "provider-call-1",
				Payload:         rawjson.Message(`{}`),
			},
			{
				Name:            "svc.tools.b",
				ToolCallID:      "runtime-call-2",
				ModelToolCallID: "provider-call-2",
				Payload:         rawjson.Message(`{}`),
			},
		},
	})

	require.NoError(t, rt.publishAwaitToolUses(t.Context(), input, base, "turn-1", item, 0))
	require.Empty(t, base.Messages)
}

func TestValidatePlannerToolCallIDsRejectsInvalidAwaitIdentity(t *testing.T) {
	tests := []struct {
		name   string
		result *planner.PlanResult
		err    string
	}{
		{
			name: "questions missing provider ID",
			result: &planner.PlanResult{Await: planner.NewAwait(planner.AwaitQuestionsItem(
				&planner.AwaitQuestions{},
			))},
			err: "planner await item 0 questions is missing model tool call ID",
		},
		{
			name: "questions pre-populates runtime ID",
			result: &planner.PlanResult{Await: planner.NewAwait(planner.AwaitQuestionsItem(
				&planner.AwaitQuestions{ToolCallID: "runtime-call", ModelToolCallID: "provider-call"},
			))},
			err: "planner await item 0 questions must not set runtime tool call ID",
		},
		{
			name: "external tool missing provider ID",
			result: &planner.PlanResult{Await: planner.NewAwait(planner.AwaitExternalToolsItem(
				&planner.AwaitExternalTools{Items: []planner.AwaitToolItem{{Name: "svc.tools.a"}}},
			))},
			err: `planner await item 0 external tool 0 is missing model tool call ID`,
		},
		{
			name: "duplicate provider ID across awaits",
			result: &planner.PlanResult{Await: planner.NewAwait(
				planner.AwaitQuestionsItem(&planner.AwaitQuestions{ModelToolCallID: "provider-call"}),
				planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{Items: []planner.AwaitToolItem{{
					Name:            "svc.tools.a",
					ModelToolCallID: "provider-call",
				}}}),
			)},
			err: `planner await item 1 external tool 0 repeats model tool call ID "provider-call"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.EqualError(t, validatePlannerToolCallIDs(test.result), test.err)
		})
	}
}

func TestCompilePlannerAwaitForRunAssignsStableDistinctIDsAndClonesPayloads(t *testing.T) {
	questionPayload := rawjson.Message(`{"question":"original"}`)
	firstPayload := rawjson.Message(`{"item":"first"}`)
	secondPayload := rawjson.Message(`{"item":"second"}`)
	source := planner.NewAwait(
		planner.AwaitQuestionsItem(&planner.AwaitQuestions{
			ID:              "questions",
			ToolName:        "assistant.ask_question",
			ModelToolCallID: "provider-question",
			Payload:         questionPayload,
			Questions: []planner.AwaitQuestion{{
				ID:      "question",
				Prompt:  "Choose",
				Options: []planner.AwaitQuestionOption{{ID: "one", Label: "One"}},
			}},
		}),
		planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
			ID: "external",
			Items: []planner.AwaitToolItem{
				{Name: "svc.tools.a", ModelToolCallID: "provider-a", Payload: firstPayload},
				{Name: "svc.tools.a", ModelToolCallID: "provider-b", Payload: secondPayload},
			},
		}),
	)
	runContext := run.Context{RunID: "run-1", TurnID: "turn-1", Attempt: 2}

	first, err := compilePlannerAwaitForRun(runContext, 1, source)
	require.NoError(t, err)
	replayed, err := compilePlannerAwaitForRun(runContext, 1, source)
	require.NoError(t, err)

	question := first.Items[0].Questions
	external := first.Items[1].ExternalTools
	require.Equal(t, generateDeterministicToolCallID("run-1", "turn-1", 2, question.ToolName, 1), question.ToolCallID)
	require.Equal(t, "provider-question", question.ModelToolCallID)
	require.Equal(t, generateDeterministicToolCallID("run-1", "turn-1", 2, external.Items[0].Name, 2), external.Items[0].ToolCallID)
	require.Equal(t, generateDeterministicToolCallID("run-1", "turn-1", 2, external.Items[1].Name, 3), external.Items[1].ToolCallID)
	require.NotEqual(t, external.Items[0].ToolCallID, external.Items[1].ToolCallID)
	require.Equal(t, question.ToolCallID, replayed.Items[0].Questions.ToolCallID)
	require.Equal(t, external.Items[0].ToolCallID, replayed.Items[1].ExternalTools.Items[0].ToolCallID)
	require.Equal(t, external.Items[1].ToolCallID, replayed.Items[1].ExternalTools.Items[1].ToolCallID)
	require.Empty(t, source.Items[0].Questions.ToolCallID)
	require.Empty(t, source.Items[1].ExternalTools.Items[0].ToolCallID)

	questionPayload[2] = 'X'
	firstPayload[2] = 'X'
	source.Items[0].Questions.Questions[0].Options[0].Label = "Changed"
	require.JSONEq(t, `{"question":"original"}`, string(question.Payload))
	require.JSONEq(t, `{"item":"first"}`, string(external.Items[0].Payload))
	require.Equal(t, "One", question.Questions[0].Options[0].Label)
}
