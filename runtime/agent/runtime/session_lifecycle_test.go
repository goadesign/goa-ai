package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/run"
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

func (e *recordingCancelerEngine) CancelByID(ctx context.Context, workflowID string) error {
	_ = ctx
	e.mu.Lock()
	e.canceled = append(e.canceled, workflowID)
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

func TestDeleteSession_CancelsActiveRuns(t *testing.T) {
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

func TestDeleteSessionEndsSessionDespiteCancellationFailure(t *testing.T) {
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

	eng := &recordingCancelerEngine{
		Engine: engineinmem.New(),
		err:    errors.New("cancel unavailable"),
	}
	rt := New(
		WithEngine(eng),
		WithLogger(telemetry.NoopLogger{}),
		WithSessionStore(store),
	)

	ended, err := rt.DeleteSession(ctx, "sess-1")

	// Engine cancellation is an optimization, not a correctness path: the
	// session is durably ended regardless, and the caller is given no retry
	// obligation it could act on.
	require.NoError(t, err)
	assert.Equal(t, session.StatusEnded, ended.Status)
}

// The planner activity is the turn-boundary lifecycle gate: a run whose
// durable session was ended must not plan another turn, and the refusal
// records the canonical cancellation provenance so the terminal RunCompleted
// event carries it even when engine cancellation never delivered.
func TestPlanActivityRefusesEndedSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := New(WithLogger(telemetry.NoopLogger{}))
	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, rt.SessionStore.UpsertRun(ctx, session.RunMeta{
		AgentID:   "agent.chat",
		RunID:     "run-1",
		SessionID: "sess-1",
		Status:    session.RunStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}))
	_, err = rt.SessionStore.EndSession(ctx, "sess-1", now)
	require.NoError(t, err)

	out, err := rt.PlanStartActivity(ctx, &PlanActivityInput{
		AgentID: "agent.chat",
		RunID:   "run-1",
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
		},
	})

	require.NoError(t, err)
	assert.True(t, out.SessionEnded)
	assert.Nil(t, out.Result)
	meta, err := rt.SessionStore.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, run.CancellationReasonSessionEnded, meta.Metadata[runMetaCancellationReason])
}

// The workflow terminates a session-ended plan refusal through the canonical
// cancellation path: runPlanActivity surfaces an error that classifies the
// run as canceled.
func TestRunPlanActivityMapsSessionEndedToCancellation(t *testing.T) {
	t.Parallel()

	rt := New(WithLogger(telemetry.NoopLogger{}))
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: "run-1",
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			//nolint:unparam // the route signature requires the error result.
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				return &PlanActivityOutput{SessionEnded: true}, nil
			},
		},
	}

	_, err := rt.runPlanActivity(wfCtx, "plan", engine.ActivityOptions{}, PlanActivityInput{
		AgentID: "agent.chat",
		RunID:   "run-1",
	}, time.Time{})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, runStatusCanceled, terminalRunStatusForError(err))
}
