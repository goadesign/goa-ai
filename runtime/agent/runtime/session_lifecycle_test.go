package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type recordingCancelerEngine struct {
	engine.Engine

	mu       sync.Mutex
	canceled []string
	err      error
}

func (e *recordingCancelerEngine) CancelByID(ctx context.Context, runID string) error {
	_ = ctx
	e.mu.Lock()
	e.canceled = append(e.canceled, runID)
	e.mu.Unlock()
	return e.err
}

func (e *recordingCancelerEngine) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.canceled))
	copy(out, e.canceled)
	return out
}

func TestDeleteSession_CancelsActiveRunsBestEffort(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := sessioninmem.New()
	now := time.Now().UTC()
	_, err := store.CreateSession(ctx, "sess-1", now)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRun(ctx, session.RunMeta{
		AgentID:   "agent.chat",
		RunID:     "run-1",
		SessionID: "sess-1",
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}))
	require.NoError(t, store.UpsertRun(ctx, session.RunMeta{
		AgentID:   "agent.chat",
		RunID:     "run-2",
		SessionID: "sess-1",
		Status:    session.RunStatusCompleted,
		StartedAt: now,
		UpdatedAt: now,
	}))
	require.NoError(t, store.UpsertRun(ctx, session.RunMeta{
		AgentID:   "agent.chat",
		RunID:     "run-3",
		SessionID: "sess-1",
		Status:    session.RunStatusPending,
		StartedAt: now,
		UpdatedAt: now,
	}))

	eng := &recordingCancelerEngine{Engine: engineinmem.New()}
	rt := New(
		WithEngine(eng),
		WithLogger(telemetry.NoopLogger{}),
		WithSessionStore(store),
	)

	ended, err := rt.DeleteSession(ctx, "sess-1")
	require.NoError(t, err)
	require.Equal(t, session.StatusEnded, ended.Status)

	canceled := eng.snapshot()
	require.ElementsMatch(t, []string{"run-1", "run-3"}, canceled)
}
