package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
)

func TestLinkChildRunValidationErrors(t *testing.T) {
	t.Parallel()

	store := New()
	err := store.LinkChildRun(context.Background(), "", session.RunMeta{
		RunID:     "run-child",
		AgentID:   "agent.child",
		SessionID: "sess-1",
		Status:    session.RunStatusPending,
	})
	require.ErrorIs(t, err, session.ErrParentRunIDRequired)
}

func TestLinkChildRunReturnsSessionMismatchError(t *testing.T) {
	t.Parallel()

	store := New()
	now := time.Now().UTC()
	sess1, err := store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)
	require.Equal(t, "sess-1", sess1.ID)
	_, err = store.CreateSession(context.Background(), "sess-2", now)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		RunID:     "run-parent",
		AgentID:   "agent.parent",
		SessionID: "sess-1",
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}))

	err = store.LinkChildRun(context.Background(), "run-parent", session.RunMeta{
		RunID:     "run-child",
		AgentID:   "agent.child",
		SessionID: "sess-2",
		Status:    session.RunStatusPending,
	})
	require.ErrorIs(t, err, session.ErrRunSessionMismatch)
}

func TestRunSuspensionIsImmutableAndIdempotent(t *testing.T) {
	t.Parallel()

	store := New()
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		RunID: "run-1", AgentID: "agent.chat", SessionID: "session-1",
	}))
	suspension := session.RunSuspension{ID: "suspension-1", Data: []byte(`{"checkpoint":"one"}`)}
	require.NoError(t, store.SaveRunSuspension(context.Background(), "run-1", suspension))
	require.NoError(t, store.SaveRunSuspension(context.Background(), "run-1", suspension))

	loaded, err := store.LoadRunSuspension(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, suspension, loaded)
	loaded.Data[0] = 'x'
	again, err := store.LoadRunSuspension(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, suspension, again)

	err = store.SaveRunSuspension(context.Background(), "run-1", session.RunSuspension{
		ID: "suspension-2", Data: []byte(`{"checkpoint":"two"}`),
	})
	require.ErrorIs(t, err, session.ErrRunSuspensionConflict)
}

func TestRunSuspensionDistinguishesMissingRunAndMissingSuspension(t *testing.T) {
	t.Parallel()

	store := New()
	_, err := store.LoadRunSuspension(context.Background(), "missing")
	require.ErrorIs(t, err, session.ErrRunNotFound)
	err = store.SaveRunSuspension(context.Background(), "missing", session.RunSuspension{
		ID: "suspension-1", Data: []byte(`{}`),
	})
	require.ErrorIs(t, err, session.ErrRunNotFound)

	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		RunID: "run-1", AgentID: "agent.chat", SessionID: "session-1",
	}))
	_, err = store.LoadRunSuspension(context.Background(), "run-1")
	require.ErrorIs(t, err, session.ErrRunSuspensionNotFound)
}

func TestPurgeSessionRemovesOnlyOwnedRunsAndSuspensions(t *testing.T) {
	t.Parallel()

	store := New()
	now := time.Now().UTC()
	for _, sessionID := range []string{"session-1", "session-2"} {
		_, err := store.CreateSession(context.Background(), sessionID, now)
		require.NoError(t, err)
		require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
			RunID: "run-" + sessionID, AgentID: "agent.chat", SessionID: sessionID,
		}))
		require.NoError(t, store.SaveRunSuspension(context.Background(), "run-"+sessionID, session.RunSuspension{
			ID: "suspension-" + sessionID, Data: []byte(`{}`),
		}))
	}

	require.NoError(t, store.PurgeSession(context.Background(), "session-1"))
	require.NoError(t, store.PurgeSession(context.Background(), "session-1"))
	_, err := store.LoadSession(context.Background(), "session-1")
	require.ErrorIs(t, err, session.ErrSessionNotFound)
	_, err = store.LoadRunSuspension(context.Background(), "run-session-1")
	require.ErrorIs(t, err, session.ErrRunNotFound)
	_, err = store.LoadRunSuspension(context.Background(), "run-session-2")
	require.NoError(t, err)
}
