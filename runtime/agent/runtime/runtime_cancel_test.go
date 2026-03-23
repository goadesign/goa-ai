package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
)

type recordingCancelByIDEngine struct {
	engine.Engine

	mu       sync.Mutex
	canceled []string
	err      error
}

func (e *recordingCancelByIDEngine) CancelByID(ctx context.Context, workflowID string) error {
	_ = ctx
	e.mu.Lock()
	e.canceled = append(e.canceled, workflowID)
	e.mu.Unlock()
	return e.err
}

func (e *recordingCancelByIDEngine) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.canceled))
	copy(out, e.canceled)
	return out
}

func TestCancelRun_CancelsByID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eng := &recordingCancelByIDEngine{Engine: engineinmem.New()}
	store := sessioninmem.New()
	require.NoError(t, store.UpsertRun(ctx, session.RunMeta{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "session-1",
		Status:    session.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	}))
	rt := New(WithEngine(eng), WithSessionStore(store))

	err := rt.CancelRun(ctx, CancelRequest{
		RunID:  "run-1",
		Reason: run.CancellationReasonUserRequested,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"run-1"}, eng.snapshot())

	meta, err := store.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.CancellationReasonUserRequested, meta.Metadata[runMetaCancellationReason])
}

func TestCancelRun_IgnoresWorkflowNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eng := &recordingCancelByIDEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowNotFound,
	}
	rt := New(WithEngine(eng))

	err := rt.CancelRun(ctx, CancelRequest{
		RunID:  "run-1",
		Reason: run.CancellationReasonUserRequested,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"run-1"}, eng.snapshot())
}

func TestCancelRun_DoesNotLeaveCancellationMetadataWhenWorkflowNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eng := &recordingCancelByIDEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowNotFound,
	}
	store := sessioninmem.New()
	require.NoError(t, store.UpsertRun(ctx, session.RunMeta{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "session-1",
		Status:    session.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	}))
	rt := New(WithEngine(eng), WithSessionStore(store))

	err := rt.CancelRun(ctx, CancelRequest{
		RunID:  "run-1",
		Reason: run.CancellationReasonUserRequested,
	})
	require.NoError(t, err)

	meta, err := store.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Nil(t, meta.Metadata)
}

func TestCancelRun_RequiresRunID(t *testing.T) {
	t.Parallel()

	rt := New(WithEngine(engineinmem.New()))
	err := rt.CancelRun(context.Background(), CancelRequest{})
	require.Error(t, err)
}

func TestCancelRun_RequiresReason(t *testing.T) {
	t.Parallel()

	rt := New(WithEngine(engineinmem.New()))
	err := rt.CancelRun(context.Background(), CancelRequest{RunID: "run-1"})
	require.Error(t, err)
}
