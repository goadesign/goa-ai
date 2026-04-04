package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type failingStreamSink struct {
	err error
}

type countingStreamSink struct {
	count int
}

func (s failingStreamSink) Send(ctx context.Context, event stream.Event) error {
	return s.err
}

func (s failingStreamSink) Close(ctx context.Context) error {
	return nil
}

func (s *countingStreamSink) Send(ctx context.Context, event stream.Event) error {
	s.count++
	return nil
}

func (s *countingStreamSink) Close(ctx context.Context) error {
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

	input, err := hooks.EncodeToRecordInput(
		hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil),
		hooks.EncodeOptions{
			TurnID:      "turn-1",
			EventKey:    "evt-stream-fail-active",
			TimestampMS: 1,
		},
	)
	require.NoError(t, err)

	err = rt.recordActivity(context.Background(), input)
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

	input, err := hooks.EncodeToRecordInput(
		hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil),
		hooks.EncodeOptions{
			TurnID:      "turn-1",
			EventKey:    "evt-stream-fail-ended",
			TimestampMS: 2,
		},
	)
	require.NoError(t, err)

	err = rt.recordActivity(context.Background(), input)
	require.NoError(t, err)

	// runlog append remains canonical even after session end.
	require.Len(t, rl.events, 1)
	require.Equal(t, "run-1", rl.events[0].RunID)
	require.Equal(t, hooks.PlannerNote, rl.events[0].Type)
}

func TestRecordActivity_TranscriptDeltaBypassesHookFanout(t *testing.T) {
	t.Parallel()

	rl := &recordingRunlog{}
	bus := hooks.NewBus()
	sink := &countingStreamSink{}
	sub, err := stream.NewSubscriber(sink)
	require.NoError(t, err)

	published := false
	busSub, err := bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		published = true
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = busSub.Close() })

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore:    rl,
		Bus:              bus,
		streamSubscriber: sub,
	}

	err = rt.recordActivity(context.Background(), &runlog.ActivityInput{
		Type:        transcript.RunLogMessagesAppended,
		EventKey:    "evt-transcript",
		RunID:       "run-1",
		AgentID:     "svc.agent",
		SessionID:   "sess-1",
		TurnID:      "turn-1",
		TimestampMS: 1,
		Payload:     payload,
	})
	require.NoError(t, err)
	require.Len(t, rl.events, 1)
	require.False(t, published)
	require.Equal(t, 0, sink.count)
}

var _ runlog.Store = (*recordingRunlog)(nil)
var _ session.Store = (*sessioninmem.Store)(nil)
