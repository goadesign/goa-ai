// Public input reads follow the exact first saved child relationship without
// importing the child provider's current tool definitions or starting work.
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genruninput "goa.design/goa-ai/registry/gen/runinput"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestRequestedInputFollowsExactSavedGrandchild(t *testing.T) {
	leaf := suspensionContractFixtureWithContext(t, "leaf.lookup", "leaf.agent", "leaf-run", nil, nil)
	rewriteSuspensionCheckpointAndPublic(t, leaf, func(saved *workflowCheckpoint) {
		saved.Pending[0].Await.Clarification.ID = "leaf-question"
		saved.Pending[0].Await.Clarification.Question = "Which record?"
	})
	middleTool := newAnyJSONSpec("middle.delegate")
	middleTool.IsAgentTool = true
	middleTool.AgentID = "leaf.agent"
	rootTool := newAnyJSONSpec("root.delegate")
	rootTool.IsAgentTool = true
	rootTool.AgentID = "middle.agent"
	middle := nestedChildSuspensionFixture(t, "middle.agent", "middle-run", middleTool, leaf)
	root := nestedChildSuspensionFixture(t, "root.agent", "root-run", rootTool, middle)
	data, err := json.Marshal(root)
	require.NoError(t, err)
	store := suspensionReadStore{run: session.RunMeta{RunID: "root-run", SessionID: "session-1", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("root.agent")}, Status: session.RunStatusSuspended}, suspension: session.RunSuspension{ID: root.ID, Data: data}}
	runtime := New(store)
	question, err := runtime.GetRequestedInput(t.Context(), "root-run")
	require.NoError(t, err)
	text, ok := question.Request.AsClarification()
	require.True(t, ok)
	assert.Equal(t, "Which record?", text.Question)
	answer, err := runtime.PrepareInputResponse(t.Context(), "root-run", &genruninput.InputResponse{Answer: genruninput.NewRunInputAnswerClarification("record A")})
	require.NoError(t, err)
	assert.Equal(t, "leaf-question", answer.Clarification.ID)
	assert.Equal(t, "record A", answer.Clarification.Answer)
	assert.Empty(t, runtime.agents)
}
