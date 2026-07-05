package runtime

// workflow_await_queue_test.go covers the await-path fix for tool-call
// thought signatures: admitAwaitItem constructs planner.ToolRequest values
// from planner.AwaitQuestions/AwaitExternalTools payloads and records them via
// recordAssistantTurn. Before the runtime-owned side carry, these
// constructions could never populate a signature (the classic drop-bug this
// refactor eliminates). Now the lookup happens against st.ToolCallSignatures
// by ToolCallID, so these await-originated tool_use parts pick up any
// signature the runtime captured for the same ID during the plan activity
// that produced the await.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestAdmitAwaitItemQuestionsAttachesCapturedToolCallSignature(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("chat.ask_question", "chat"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1", SessionID: "sess-1"}}
	input := &RunInput{AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "sess-1"}
	st := &runLoopState{ToolCallSignatures: map[string]string{"call-1": "opaque-provider-signature"}}
	item := planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID:         "await-1",
		ToolName:   "chat.ask_question",
		ToolCallID: "call-1",
		Payload:    rawjson.Message(`{}`),
		Questions:  []planner.AwaitQuestion{{ID: "q1", Prompt: "which?"}},
	})

	require.NoError(t, rt.admitAwaitItem(t.Context(), input, base, st, "turn-1", item, 0))

	require.Len(t, base.Messages, 1)
	require.Len(t, base.Messages[0].Parts, 1)
	use, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", use.ID)
	require.Equal(t, "opaque-provider-signature", use.ThoughtSignature)
}

func TestAdmitAwaitItemExternalToolsAttachesCapturedToolCallSignaturePerItem(t *testing.T) {
	rt := New()
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.a", "svc.tools"), newAnyJSONSpec("svc.tools.b", "svc.tools"))
	base := &planner.PlanInput{RunContext: run.Context{RunID: "run-1", SessionID: "sess-1"}}
	input := &RunInput{AgentID: agent.Ident("agent-1"), RunID: "run-1", SessionID: "sess-1"}
	st := &runLoopState{ToolCallSignatures: map[string]string{"call-1": "sig-1"}} // call-2 uncaptured
	item := planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
		ID: "await-1",
		Items: []planner.AwaitToolItem{
			{Name: "svc.tools.a", ToolCallID: "call-1", Payload: rawjson.Message(`{}`)},
			{Name: "svc.tools.b", ToolCallID: "call-2", Payload: rawjson.Message(`{}`)},
		},
	})

	require.NoError(t, rt.admitAwaitItem(t.Context(), input, base, st, "turn-1", item, 0))

	require.Len(t, base.Messages, 1)
	require.Len(t, base.Messages[0].Parts, 2)
	first, ok := base.Messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-1", first.ID)
	require.Equal(t, "sig-1", first.ThoughtSignature)
	second, ok := base.Messages[0].Parts[1].(model.ToolUsePart)
	require.True(t, ok)
	require.Equal(t, "call-2", second.ID)
	require.Empty(t, second.ThoughtSignature)
}
