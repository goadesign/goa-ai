package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
)

type recordingRunlog struct {
	events []*runlog.Event
	err    error
}

func (r *recordingRunlog) Append(_ context.Context, e *runlog.Event) error {
	if r.err != nil {
		return r.err
	}
	if e == nil {
		return errors.New("event is nil")
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recordingRunlog) List(context.Context, string, string, int) (runlog.Page, error) {
	return runlog.Page{}, errors.New("not implemented")
}

func TestHookActivityAppendsBeforePublish(t *testing.T) {
	t.Parallel()

	rl := &recordingRunlog{}
	bus := hooks.NewBus()
	store := sessioninmem.New()

	var published hooks.Event
	sub, err := bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		published = evt
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })

	rt := &Runtime{
		RunEventStore: rl,
		Bus:           bus,
		SessionStore:  store,
	}

	now := time.Now().UTC()
	_, err = store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		Status:    session.RunStatusPending,
		StartedAt: now,
		UpdatedAt: now,
		Labels:    nil,
		Metadata:  nil,
	}))

	input, err := hooks.EncodeToHookInput(hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil), "turn-1")
	require.NoError(t, err)

	err = rt.hookActivity(context.Background(), input)
	require.NoError(t, err)

	require.NotNil(t, published)
	require.Len(t, rl.events, 1)
	require.Equal(t, "run-1", rl.events[0].RunID)
	require.Equal(t, hooks.PlannerNote, rl.events[0].Type)
	require.Equal(t, input.Payload, rl.events[0].Payload)
}

func TestHookActivityAppendFailureAbortsPublish(t *testing.T) {
	t.Parallel()

	appendErr := errors.New("append failed")
	rl := &recordingRunlog{err: appendErr}
	bus := hooks.NewBus()
	store := sessioninmem.New()

	var published hooks.Event
	sub, err := bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		published = evt
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })

	rt := &Runtime{
		RunEventStore: rl,
		Bus:           bus,
		SessionStore:  store,
	}

	now := time.Now().UTC()
	_, err = store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRun(context.Background(), session.RunMeta{
		AgentID:   "svc.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		Status:    session.RunStatusPending,
		StartedAt: now,
		UpdatedAt: now,
		Labels:    nil,
		Metadata:  nil,
	}))

	input, err := hooks.EncodeToHookInput(hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil), "turn-1")
	require.NoError(t, err)

	err = rt.hookActivity(context.Background(), input)
	require.ErrorIs(t, err, appendErr)
	require.Nil(t, published)
}
