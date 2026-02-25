package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
)

func TestResolvePromptRefsIncludesChildRuns(t *testing.T) {
	t.Parallel()

	store := sessioninmem.New()
	now := time.Now().UTC()
	_, err := store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)

	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.parent",
		RunID:     "run-1",
		SessionID: "sess-1",
		Status:    session.RunStatusCompleted,
		StartedAt: now,
		UpdatedAt: now,
		PromptRefs: []prompt.PromptRef{
			{ID: prompt.Ident("prompt.a"), Version: "v1"},
			{ID: prompt.Ident("prompt.b"), Version: "v1"},
		},
		ChildRunIDs: []string{"run-2", "run-3"},
	}))
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.child",
		RunID:     "run-2",
		SessionID: "sess-1",
		Status:    session.RunStatusCompleted,
		StartedAt: now,
		UpdatedAt: now,
		PromptRefs: []prompt.PromptRef{
			{ID: prompt.Ident("prompt.c"), Version: "v1"},
			{ID: prompt.Ident("prompt.a"), Version: "v1"},
		},
		ChildRunIDs: []string{"run-4"},
	}))
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.child",
		RunID:     "run-3",
		SessionID: "sess-1",
		Status:    session.RunStatusCompleted,
		StartedAt: now,
		UpdatedAt: now,
		PromptRefs: []prompt.PromptRef{
			{ID: prompt.Ident("prompt.d"), Version: "v1"},
		},
	}))
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.child",
		RunID:     "run-4",
		SessionID: "sess-1",
		Status:    session.RunStatusCompleted,
		StartedAt: now,
		UpdatedAt: now,
		PromptRefs: []prompt.PromptRef{
			{ID: prompt.Ident("prompt.e"), Version: "v2"},
		},
		// Cycle to validate traversal remains bounded.
		ChildRunIDs: []string{"run-1"},
	}))

	refs, err := session.ResolvePromptRefs(context.Background(), store, "run-1")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: prompt.Ident("prompt.a"), Version: "v1"},
		{ID: prompt.Ident("prompt.b"), Version: "v1"},
		{ID: prompt.Ident("prompt.c"), Version: "v1"},
		{ID: prompt.Ident("prompt.d"), Version: "v1"},
		{ID: prompt.Ident("prompt.e"), Version: "v2"},
	}, refs)
}

func TestResolvePromptRefsMissingRootRun(t *testing.T) {
	t.Parallel()

	store := sessioninmem.New()
	_, err := session.ResolvePromptRefs(context.Background(), store, "missing")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestResolvePromptRefsAfterAtomicLinkChildRun(t *testing.T) {
	t.Parallel()

	store := sessioninmem.New()
	now := time.Now().UTC()
	_, err := store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.parent",
		RunID:     "run-parent",
		SessionID: "sess-1",
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
		PromptRefs: []prompt.PromptRef{
			{ID: prompt.Ident("prompt.parent"), Version: "v1"},
		},
	}))
	require.NoError(t, store.LinkChildRun(context.Background(), "run-parent", session.RunMeta{
		AgentID:   "svc.child",
		RunID:     "run-child",
		SessionID: "sess-1",
		Status:    session.RunStatusPending,
		PromptRefs: []prompt.PromptRef{
			{ID: prompt.Ident("prompt.child"), Version: "v1"},
		},
	}))

	refs, err := session.ResolvePromptRefs(context.Background(), store, "run-parent")
	require.NoError(t, err)
	require.Equal(t, []prompt.PromptRef{
		{ID: prompt.Ident("prompt.parent"), Version: "v1"},
		{ID: prompt.Ident("prompt.child"), Version: "v1"},
	}, refs)
}
