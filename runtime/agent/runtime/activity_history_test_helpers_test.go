package runtime

// Activity tests publish their input messages through the same store contract
// as workflows. Wire inputs contain only the resulting committed position.

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func seedTestPlanInput(t testing.TB, rt *Runtime, input PlanActivityInput, messages []*model.Message) *PlanActivityInput {
	t.Helper()
	if input.RunID == "" {
		input.RunID = "test-run"
	}
	if input.RunContext.RunID == "" {
		input.RunContext.RunID = input.RunID
	}
	if input.HistoryEndID != "" {
		require.Empty(t, messages, "an existing position cannot be replaced with another history")
		return &input
	}
	_, err := rt.Store.LoadRun(t.Context(), input.RunID)
	if errors.Is(err, session.ErrRunNotFound) {
		admitRunForTest(t, rt.Store, session.RunMeta{
			AgentID: string(input.AgentID), RunID: input.RunID, SessionID: input.RunContext.SessionID,
			Status: session.RunStatusRunning,
		})
	} else {
		require.NoError(t, err)
	}
	if len(messages) == 0 {
		page, err := rt.Store.ListRunRecords(t.Context(), input.RunID, "", 1)
		require.NoError(t, err)
		require.Len(t, page.Events, 1)
		input.HistoryEndID = page.Events[0].ID
	} else {
		input.HistoryEndID = appendTestActivityHistory(t, rt, &input, messages)
	}
	return &input
}

func appendTestActivityHistory(t testing.TB, rt *Runtime, input *PlanActivityInput, messages []*model.Message) string {
	t.Helper()
	payload, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)
	result, err := rt.Store.AppendRunRecord(t.Context(), &runlog.Event{
		RunID: input.RunID, AgentID: input.AgentID, SessionID: input.RunContext.SessionID,
		EventKey: uuid.NewString(), Timestamp: time.Now().Truncate(time.Millisecond),
		Type: transcript.RunLogMessagesAppended, Payload: payload,
	})
	require.NoError(t, err)
	return result.ID
}

func testActivityMessages(t testing.TB, rt *Runtime, input *PlanActivityInput) []*model.Message {
	t.Helper()
	messages, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), rt.Store, input.RunID, input.HistoryEndID)
	require.NoError(t, err)
	return messages
}

func testResolvedPlanInput(t testing.TB, rt *Runtime, input *PlanActivityInput) *resolvedPlanActivityInput {
	t.Helper()
	seeded := seedTestPlanInput(t, rt, *input, nil)
	return &resolvedPlanActivityInput{PlanActivityInput: seeded, Messages: testActivityMessages(t, rt, seeded)}
}

func testToolHistory(t testing.TB, rt *Runtime, agentID agent.Ident, parent run.Context, messages []*model.Message) string {
	t.Helper()
	return seedTestPlanInput(t, rt, PlanActivityInput{AgentID: agentID, RunID: parent.RunID, RunContext: parent}, messages).HistoryEndID
}

func seedTestContinuationHistory(t testing.TB, rt *Runtime, input *RunInput, checkpoint *workflowCheckpoint) string {
	t.Helper()
	return seedTestPlanInput(t, rt, PlanActivityInput{
		AgentID: input.AgentID, RunID: input.RunID,
		RunContext: restoreCheckpointRunContext(checkpoint.Context, input),
	}, checkpoint.BaseMessages).HistoryEndID
}
