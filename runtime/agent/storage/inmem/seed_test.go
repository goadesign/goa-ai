package inmem

// These tests exercise preparation before engine acceptance, including exact
// retries and deletion. They never start planner or model work.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestSeedPublicationAndExactRetry(t *testing.T) {
	store, start := activeRunStore(t)
	d := storage.SeedDeclaration{AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral}
	first, err := store.BeginRunSeed(t.Context(), d)
	require.NoError(t, err)
	require.Equal(t, storage.EmptySeedEndID, first.EndID)
	_, err = store.LoadRun(t.Context(), start.RunID)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	_, err = store.LoadRunSeed(t.Context(), start.RunID, first.EndID)
	require.ErrorIs(t, err, storage.ErrSeedNotFound)
	_, err = store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.ErrorIs(t, err, storage.ErrSeedNotFound)

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "new input"}},
	}})
	require.NoError(t, err)
	command := storage.SeedAppend{RunID: start.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{
		Key: "first", PreviousID: first.EndID, Messages: payload,
	}}
	end, err := store.AppendRunSeed(t.Context(), command)
	require.NoError(t, err)
	retry, err := store.AppendRunSeed(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, end, retry)
	require.ErrorIs(t, store.PublishRunSeed(t.Context(), storage.SeedPublication{RunID: start.RunID, AttemptID: d.AttemptID, SeedEndID: first.EndID, EndID: end, PreparedBytes: 2}), storage.ErrSeedConflict)
	publication := appendSeedTestCompletion(t, store, d, end)
	require.NoError(t, store.PublishRunSeed(t.Context(), publication))
	require.NoError(t, store.PublishRunSeed(t.Context(), publication))
	retry, err = store.AppendRunSeed(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, end, retry)
	command.Record.Key = "other"
	command.Record.PreviousID = end
	_, err = store.AppendRunSeed(t.Context(), command)
	require.ErrorIs(t, err, storage.ErrSeedConflict)

	start.SeedEndID = end
	result, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.NoError(t, err)
	actual, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, start.RunID, result.Started.ID)
	require.NoError(t, err)
	require.Equal(t, "new input", actual[0].Text())
	require.Len(t, store.records[start.RunID], 1, "start attaches the seed without copying messages")
	_, err = store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.NoError(t, err)
}

func TestSeedPurgeRemovesUnstartedPublication(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(fmt.Sprint(publish), func(t *testing.T) {
			store, start := activeRunStore(t)
			d := storage.SeedDeclaration{AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral}
			_, err := store.BeginRunSeed(t.Context(), d)
			require.NoError(t, err)
			if publish {
				require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, d, storage.EmptySeedEndID)))
			}
			_, err = store.EndSession(t.Context(), start.SessionID, time.Now())
			require.NoError(t, err)
			require.NoError(t, store.PurgeSession(t.Context(), start.SessionID))
			require.Empty(t, store.seeds)
			_, err = store.BeginRunSeed(t.Context(), d)
			require.ErrorIs(t, err, session.ErrSessionPurged)
			start.SeedEndID = storage.EmptySeedEndID
			_, err = store.StartRootRun(t.Context(), rootStartCommand(t, start))
			require.ErrorIs(t, err, session.ErrSessionPurged)
		})
	}
}

func TestSeedCapturedSourceAndSingleReference(t *testing.T) {
	store, source := activeRunStore(t)
	source = startSeedTestRun(t, store, source)
	result, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: source.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "finished", source, "success", nil),
	})
	require.NoError(t, err)
	d := storage.SeedDeclaration{
		AgentID: source.AgentID, RunID: "next", SessionID: source.SessionID, CommandID: "command", AttemptID: "attempt",
		Kind: storage.SeedNextTurn, SourceRunID: source.RunID, ExcludeReasoning: true,
	}
	seed, err := store.BeginRunSeed(t.Context(), d)
	require.NoError(t, err)
	require.Equal(t, &storage.HistoryPrefix{
		RunID: source.RunID, EndID: result.ID, ExcludeSystem: true, ExcludeReasoning: true,
	}, seed.Source)
	require.ErrorIs(t, store.PublishRunSeed(t.Context(), storage.SeedPublication{RunID: d.RunID, AttemptID: d.AttemptID, SeedEndID: seed.EndID, EndID: seed.EndID, PreparedBytes: 2}), storage.ErrSeedConflict)
	end, err := store.AppendRunSeed(t.Context(), storage.SeedAppend{
		RunID: d.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{Key: "source", PreviousID: seed.EndID, Prefix: seed.Source},
	})
	require.NoError(t, err)
	_, err = store.AppendRunSeed(t.Context(), storage.SeedAppend{
		RunID: d.RunID, AttemptID: d.AttemptID, Record: storage.SeedRecord{Key: "duplicate", PreviousID: end, Prefix: seed.Source},
	})
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, d, end)))
	d.ExcludeReasoning = false
	_, err = store.BeginRunSeed(t.Context(), d)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
}

func TestSeedRejectsUnusableSources(t *testing.T) {
	for _, status := range []session.RunStatus{
		session.RunStatusRunning, session.RunStatusSuspended, session.RunStatusFailed, session.RunStatusCanceled,
	} {
		t.Run(string(status), func(t *testing.T) {
			store, source := activeRunStore(t)
			source = startSeedTestRun(t, store, source)
			// The stored state is the boundary under test, not the engine's
			// separate terminal-record construction.
			meta := store.runs[source.RunID]
			meta.Status = status
			store.runs[source.RunID] = meta
			_, err := store.BeginRunSeed(t.Context(), storage.SeedDeclaration{
				AgentID: source.AgentID, RunID: "next", SessionID: source.SessionID, CommandID: "command", AttemptID: "attempt",
				Kind: storage.SeedNextTurn, SourceRunID: source.RunID,
			})
			require.Error(t, err)
			require.NotContains(t, store.seeds, "next")
		})
	}
}

func TestSeedCancellationBeforeReadOrWrite(t *testing.T) {
	store, start := activeRunStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := store.BeginRunSeed(ctx, storage.SeedDeclaration{
		AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral,
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, store.seeds)
}

func startSeedTestRun(t *testing.T, store *Store, start session.RunStart) session.RunStart {
	t.Helper()
	d := storage.SeedDeclaration{
		AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral,
	}
	_, err := store.BeginRunSeed(t.Context(), d)
	require.NoError(t, err)
	start.SeedEndID = storage.EmptySeedEndID
	require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, d, start.SeedEndID)))
	_, err = store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.NoError(t, err)
	return start
}

func appendSeedTestCompletion(t *testing.T, store *Store, declaration storage.SeedDeclaration, historyEnd string) storage.SeedPublication {
	t.Helper()
	prepared := []byte(`{}`)
	end, err := store.AppendRunSeed(t.Context(), storage.SeedAppend{
		RunID: declaration.RunID, AttemptID: declaration.AttemptID,
		Record: storage.SeedRecord{Key: "prepared", PreviousID: historyEnd, Prepared: prepared},
	})
	require.NoError(t, err)
	return storage.SeedPublication{RunID: declaration.RunID, AttemptID: declaration.AttemptID, SeedEndID: historyEnd, EndID: end, PreparedBytes: int64(len(prepared))}
}
