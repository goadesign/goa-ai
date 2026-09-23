package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type publicationReplyLost struct {
	storage.Store
	lose bool
}

func (s *publicationReplyLost) PublishRunSeed(ctx context.Context, p storage.SeedPublication) error {
	if err := s.Store.PublishRunSeed(ctx, p); err != nil {
		return err
	}
	if s.lose {
		s.lose = false
		return context.DeadlineExceeded
	}
	return nil
}

func TestPreparationRecoveryPreservesAcceptedRequestAfterLostReply(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", nil, nil))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	client.(*agentClient).r.Store = &publicationReplyLost{Store: store, lose: true}
	original := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "original prompt"}}}}
	_, err := client.Prepare(t.Context(), "session-1", original, WithRunID("run-1"), WithPreparation("original-command", "attempt-1"), WithLabels(map[string]string{"profile": "original"}))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, eng.startCalls)
	recovered, found, err := client.SettlePrepared(t.Context(), "session-1", "run-1", "original-command", "attempt-1")
	require.NoError(t, err)
	require.True(t, found)
	history, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run-1", "")
	require.Error(t, err) // The publication is not a started run.
	require.Nil(t, history)
	data, err := recovered.MarshalBinary()
	require.NoError(t, err)
	fresh := New(store, WithEngine(eng)).MustClientFor(client.(*agentClient).definition)
	again, found, err := fresh.RecoverPrepared(t.Context(), "session-1", "run-1", "original-command")
	require.NoError(t, err)
	require.True(t, found)
	same, err := again.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, data, same)
	_, err = fresh.StartPrepared(t.Context(), again)
	require.NoError(t, err)
	require.Equal(t, 1, eng.startCalls)
	require.Equal(t, "original", eng.last.Input.Labels["profile"])
	_, _, err = fresh.RecoverPrepared(t.Context(), "session-1", "run-1", "different-command")
	require.ErrorIs(t, err, storage.ErrSeedConflict)
}

func TestPreparationAbandonmentRejectsLateBeginAndAllowsNewAttempt(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", nil, nil))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	_, found, err := client.SettlePrepared(t.Context(), "session-1", "run-1", "command", "old-attempt")
	require.NoError(t, err)
	require.False(t, found)
	_, err = client.Prepare(t.Context(), "session-1", nil, WithRunID("run-1"), WithPreparation("command", "old-attempt"))
	require.ErrorIs(t, err, storage.ErrPreparationAbandoned)
	prepared, err := client.Prepare(t.Context(), "session-1", nil, WithRunID("run-1"), WithPreparation("command", "new-attempt"), WithLabels(map[string]string{"profile": "new"}))
	require.NoError(t, err)
	_, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	require.Equal(t, "new", eng.last.Input.Labels["profile"])
	_, found, err = client.SettlePrepared(t.Context(), "session-1", "run-1", "command", "old-attempt")
	require.NoError(t, err)
	require.True(t, found)
}

func TestChildPreparationRecoversBeforeResolvingChangedPrompt(t *testing.T) {
	rt, input := childHeartbeatFixture(t)
	cfg, err := rt.selectedAgentToolConfig(input.Call)
	require.NoError(t, err)
	cfg.SystemPrompt = "original system prompt"
	first, err := rt.prepareAgentChildActivity(t.Context(), input)
	require.NoError(t, err)
	child := agentChildRunContext(&input.Call)
	before, err := rt.Store.ListRunSeedRecords(t.Context(), child.RunID, first.Success.SeedEndID, "", 512)
	require.NoError(t, err)
	cfg.SystemPrompt = "replacement system prompt"
	// Discarding the first activity result models a reply lost after publication.
	second, err := rt.prepareAgentChildActivity(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, first, second)
	after, err := rt.Store.ListRunSeedRecords(t.Context(), child.RunID, second.Success.SeedEndID, "", 512)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Contains(t, string(after.Records[0].Messages), "original system prompt")
	require.NotContains(t, string(after.Records[0].Messages), "replacement system prompt")
	input.Call.ToolCallID = "new-operation"
	newer, err := rt.prepareAgentChildActivity(t.Context(), input)
	require.NoError(t, err)
	fresh := agentChildRunContext(&input.Call)
	page, err := rt.Store.ListRunSeedRecords(t.Context(), fresh.RunID, newer.Success.SeedEndID, "", 512)
	require.NoError(t, err)
	require.Contains(t, string(page.Records[0].Messages), "replacement system prompt")
}

type appendReplyLost struct {
	storage.Store
	lose bool
}

func (s *appendReplyLost) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	end, err := s.Store.AppendRunSeed(ctx, command)
	if err != nil {
		return "", err
	}
	if s.lose {
		s.lose = false
		return "", context.DeadlineExceeded
	}
	return end, nil
}

func TestChildPreparationRetriesIncompleteBodyWithNewCandidate(t *testing.T) {
	rt, input := childHeartbeatFixture(t)
	cfg, err := rt.selectedAgentToolConfig(input.Call)
	require.NoError(t, err)
	cfg.SystemPrompt = "unfinished candidate"
	rt.Store = &appendReplyLost{Store: rt.Store, lose: true}
	_, err = rt.prepareAgentChildActivity(t.Context(), input)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, engine.IsActivityErrorNonRetryable(err))
	cfg.SystemPrompt = "current candidate"
	output, err := rt.prepareAgentChildActivity(t.Context(), input)
	require.NoError(t, err)
	child := agentChildRunContext(&input.Call)
	page, err := rt.Store.ListRunSeedRecords(t.Context(), child.RunID, output.Success.SeedEndID, "", 512)
	require.NoError(t, err)
	require.Contains(t, string(page.Records[0].Messages), "current candidate")
	require.NotContains(t, string(page.Records[0].Messages), "unfinished candidate")
}
