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

func TestAdmitAwaitItemQuestionsDoesNotDuplicateCommittedResponse(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("chat.ask_question", "chat"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1", SessionID: "sess-1"}}
	input := &RunInput{AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "sess-1"}
	item := planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID:         "await-1",
		ToolName:   "chat.ask_question",
		ToolCallID: "call-1",
		Payload:    rawjson.Message(`{}`),
		Questions:  []planner.AwaitQuestion{{ID: "q1", Prompt: "which?"}},
	})
	result := &planner.PlanResult{Await: planner.NewAwait(item)}
	transcript := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.ToolUsePart{
			ID:               "call-1",
			Name:             "chat.ask_question",
			Input:            rawjson.Message(`{}`),
			ThoughtSignature: "opaque-provider-signature",
		}},
	}}

	require.NoError(t, rt.appendSelectedModelResponse(t.Context(), input.AgentID, base, "turn-1", result, transcript))
	require.NoError(t, rt.admitAwaitItem(t.Context(), input, base, "turn-1", item, 0))

	require.Len(t, base.Messages, 1)
	require.Len(t, base.Messages[0].Parts, 1)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", use.ID)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
}

func TestAdmitAwaitItemExternalToolsDoesNotRecordAssistantResponse(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.a", "svc.tools"), newAnyJSONSpec("svc.tools.b", "svc.tools"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1", SessionID: "sess-1"}}
	input := &RunInput{AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "sess-1"}
	item := planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
		ID: "await-1",
		Items: []planner.AwaitToolItem{
			{Name: "svc.tools.a", ToolCallID: "call-1", Payload: rawjson.Message(`{}`)},
			{Name: "svc.tools.b", ToolCallID: "call-2", Payload: rawjson.Message(`{}`)},
		},
	})

	require.NoError(t, rt.admitAwaitItem(t.Context(), input, base, "turn-1", item, 0))
	require.Empty(t, base.Messages)
}
