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
