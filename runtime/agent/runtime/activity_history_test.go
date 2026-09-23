package runtime

// Activity history tests exercise the stored-owner and exact-position checks
// before planning or child validation can read messages.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

type historyReadStore struct {
	storage.Store
	loadErr     error
	prefixReads int
}

func (s *historyReadStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	if s.loadErr != nil {
		return session.RunMeta{}, s.loadErr
	}
	return s.Store.LoadRun(ctx, runID)
}

func (s *historyReadStore) ListRunTranscriptRecords(ctx context.Context, runID, throughID, afterID string, limit int) (runlog.Page, error) {
	s.prefixReads++
	return s.Store.ListRunTranscriptRecords(ctx, runID, throughID, afterID, limit)
}

func TestActivityHistoryRejectsForeignOwnersBeforeReading(t *testing.T) {
	const foreignAgent = "other.agent"
	tests := []struct {
		name   string
		change func(*PlanActivityInput)
	}{
		{"agent", func(in *PlanActivityInput) { in.AgentID = foreignAgent }},
		{"session", func(in *PlanActivityInput) { in.RunContext.SessionID = "other-session" }},
		{"run context", func(in *PlanActivityInput) { in.RunContext.RunID = "other-run" }},
		{"missing position", func(in *PlanActivityInput) { in.HistoryEndID = "" }},
		{"missing run", func(in *PlanActivityInput) {
			in.RunID, in.RunContext.RunID = "missing-run", "missing-run"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
				start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					t.Fatal("invalid history must not reach planning")
					return nil, nil
				},
			})
			input := seedTestPlanInput(t, rt, PlanActivityInput{
				AgentID: "service.agent", RunID: "run",
				RunContext: run.Context{RunID: "run", SessionID: "session"},
			}, []*model.Message{userMsg("private history")})
			store := &historyReadStore{Store: rt.Store}
			rt.Store = store
			test.change(input)
			out, err := rt.PlanStartActivity(t.Context(), input)
			require.Nil(t, out)
			require.True(t, engine.IsActivityErrorNonRetryable(err), "%v", err)
			require.Zero(t, store.prefixReads)

			child, childErr := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{
				Call:      ToolCall{AgentID: input.AgentID, RunID: input.RunID, SessionID: "session"},
				ParentRun: input.RunContext, HistoryEndID: input.HistoryEndID,
			})
			require.Nil(t, child)
			require.True(t, engine.IsActivityErrorNonRetryable(childErr), "%v", childErr)
			require.Zero(t, store.prefixReads)
		})
	}
}

func TestActivityHistoryRetriesReadTheSamePrefix(t *testing.T) {
	var got []*model.Message
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
		start: func(_ context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
			got = in.Messages
			return finalPlannerResult("done"), nil
		},
	})
	original := []*model.Message{userMsg("first input")}
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent"}, original)
	_, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	first := canonicalHistory(t, got)
	appendTestActivityHistory(t, rt, input, []*model.Message{assistantTextMsg("later answer"), userMsg("later input")})
	_, err = rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, first, canonicalHistory(t, got))
	require.Equal(t, canonicalHistory(t, original), canonicalHistory(t, got))

	input.HistoryEndID = "missing-position"
	_, err = rt.PlanStartActivity(t.Context(), input)
	require.ErrorIs(t, err, storage.ErrInvalidTranscriptPosition)
	require.True(t, engine.IsActivityErrorNonRetryable(err))
}

func TestActivityHistoryKeepsTemporaryReadErrorsRetryable(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("store unavailable"), session.ErrSessionPurged} {
		t.Run(cause.Error(), func(t *testing.T) {
			rt := New(&historyReadStore{loadErr: cause})
			_, err := rt.loadActivityHistory(t.Context(), "service.agent", "run", "session", "position")
			require.ErrorIs(t, err, cause)
			require.Equal(t, errors.Is(cause, session.ErrSessionPurged), engine.IsActivityErrorNonRetryable(err))
		})
	}
}

func TestActivityHistoryChildValidatorReceivesPendingCall(t *testing.T) {
	rt := New(newTestStore())
	parent := run.Context{RunID: "parent-run", SessionID: "session"}
	messages := []*model.Message{
		userMsg("original request"),
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{
			ID: "child-call", Name: "child.lookup", Input: rawjson.Message(`{}`),
		}}},
	}
	position := testToolHistory(t, rt, "parent.agent", parent, messages)
	validations := 0
	reg := NewAgentToolsetRegistration(AgentToolConfig{
		Definition: testAgentDefinition("child.agent", "child.workflow", "child.queue", nil, nil),
		PreChildValidator: func(_ context.Context, in *AgentToolValidationInput) *tools.ValidationError {
			validations++
			require.Equal(t, canonicalHistory(t, messages), canonicalHistory(t, in.Messages))
			return nil
		},
	})
	registerAgentToolTestConfig(rt, *reg.AgentTool, "child", newAnyJSONSpec("child.lookup"))
	appendTestActivityHistory(t, rt, &PlanActivityInput{AgentID: "parent.agent", RunID: parent.RunID, RunContext: parent}, []*model.Message{userMsg("later input")})
	out, err := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{
		Call: ToolCall{AgentID: "parent.agent", RunID: parent.RunID, SessionID: parent.SessionID,
			Name: "child.lookup", ToolCallID: "child-call", Payload: rawjson.Message(`{}`)},
		ParentRun: parent, HistoryEndID: position,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Success)
	require.Equal(t, 1, validations)

	_, err = rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID: "parent.agent", RunID: parent.RunID, RunContext: parent, HistoryEndID: position,
	})
	require.True(t, engine.IsActivityErrorNonRetryable(err), "planning must reject an unanswered tool call: %v", err)
}
