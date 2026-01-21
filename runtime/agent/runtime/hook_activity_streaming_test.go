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
	"goa.design/goa-ai/runtime/agent/stream"
)

type failingStreamSink struct {
	err error
}

func (s failingStreamSink) Send(ctx context.Context, event stream.Event) error {
	return s.err
}

func (s failingStreamSink) Close(ctx context.Context) error {
	return nil
}

func TestHookActivity_StreamFailureFailsRunWhileSessionActive(t *testing.T) {
	t.Parallel()

	streamErr := errors.New("stream send failed")
	store := sessioninmem.New()
	rl := &recordingRunlog{}

	sub, err := stream.NewSubscriber(failingStreamSink{err: streamErr})
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore:    rl,
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}

	now := time.Now().UTC()
	_, err = store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)

	input, err := hooks.EncodeToHookInput(hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil), "turn-1")
	require.NoError(t, err)

	err = rt.hookActivity(context.Background(), input)
	require.ErrorIs(t, err, streamErr)
	require.Len(t, rl.events, 1, "expected canonical run log append even when stream send fails")
}

func TestHookActivity_StreamFailureNoopAfterSessionEnded(t *testing.T) {
	t.Parallel()

	streamErr := errors.New("stream send failed")
	store := sessioninmem.New()
	rl := &recordingRunlog{}

	sub, err := stream.NewSubscriber(failingStreamSink{err: streamErr})
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore:    rl,
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}

	now := time.Now().UTC()
	_, err = store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)
	_, err = store.EndSession(context.Background(), "sess-1", now.Add(time.Second))
	require.NoError(t, err)

	input, err := hooks.EncodeToHookInput(hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil), "turn-1")
	require.NoError(t, err)

	err = rt.hookActivity(context.Background(), input)
	require.NoError(t, err)

	// runlog append remains canonical even after session end.
	require.Len(t, rl.events, 1)
	require.Equal(t, "run-1", rl.events[0].RunID)
	require.Equal(t, hooks.PlannerNote, rl.events[0].Type)
}

var _ runlog.Store = (*recordingRunlog)(nil)
var _ session.Store = (*sessioninmem.Store)(nil)
