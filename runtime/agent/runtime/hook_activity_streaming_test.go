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
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
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

type failAfterFirstKeyedPublicationSink struct {
	err    error
	events map[string]stream.Event
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

func (s *failAfterFirstKeyedPublicationSink) Send(ctx context.Context, event stream.Event) error {
	if _, exists := s.events[event.EventKey()]; exists {
		return nil
	}
	s.events[event.EventKey()] = event
	if s.err != nil {
		err := s.err
		s.err = nil
		return err
	}
	return nil
}

func (s *failAfterFirstKeyedPublicationSink) Close(ctx context.Context) error {
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

	err = rt.recordActivity(context.Background(), testRecordBatch(input))
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

	err = rt.recordActivity(context.Background(), testRecordBatch(input))
	require.NoError(t, err)

	// runlog append remains canonical even after session end.
	require.Len(t, rl.events, 1)
	require.Equal(t, "run-1", rl.events[0].RunID)
	require.Equal(t, hooks.PlannerNote, rl.events[0].Type)
}

func TestHookActivity_RetryCompletesOneKeyedStreamPublication(t *testing.T) {
	ctx := context.Background()
	store := sessioninmem.New()
	_, err := store.CreateSession(ctx, "sess-1", time.Unix(0, 0).UTC())
	require.NoError(t, err)
	sink := &failAfterFirstKeyedPublicationSink{
		err:    errors.New("post-publication callback failed"),
		events: make(map[string]stream.Event),
	}
	sub, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	rt := &Runtime{
		RunEventStore:    runloginmem.New(),
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}
	input, err := hooks.EncodeToRecordInput(
		hooks.NewPlannerNoteEvent("run-1", "svc.agent", "sess-1", "note", nil),
		hooks.EncodeOptions{
			TurnID:      "turn-1",
			EventKey:    "evt-retry",
			TimestampMS: 1,
		},
	)
	require.NoError(t, err)

	require.Error(t, rt.recordActivity(ctx, testRecordBatch(input)))
	require.NoError(t, rt.recordActivity(ctx, testRecordBatch(input)))

	page, err := rt.RunEventStore.List(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Len(t, sink.events, 1)
	require.Contains(t, sink.events, "evt-retry")
}

func TestRecordActivity_TranscriptDeltaSkipsBusAndNonAssistantStreamEvents(t *testing.T) {
	t.Parallel()

	rl := &recordingRunlog{}
	bus := hooks.NewBus()
	store := sessioninmem.New()
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
		SessionStore:     store,
		streamSubscriber: sub,
	}
	_, err = store.CreateSession(context.Background(), "sess-1", time.Now().UTC())
	require.NoError(t, err)

	err = rt.recordActivity(context.Background(), testRecordBatch(&runlog.ActivityInput{
		Type:        transcript.RunLogMessagesAppended,
		EventKey:    "evt-transcript",
		RunID:       "run-1",
		AgentID:     "svc.agent",
		SessionID:   "sess-1",
		TurnID:      "turn-1",
		TimestampMS: 1,
		Payload:     payload,
	}))
	require.NoError(t, err)
	require.Len(t, rl.events, 1)
	require.False(t, published)
	require.Equal(t, 0, sink.count)
}

func TestRecordActivity_TranscriptSeedDoesNotStreamCommittedAssistantTurns(t *testing.T) {
	t.Parallel()

	rl := &recordingRunlog{}
	store := sessioninmem.New()
	sink := &countingStreamSink{}
	sub, err := stream.NewSubscriber(sink)
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore:    rl,
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}
	_, err = store.CreateSession(context.Background(), "sess-1", time.Now().UTC())
	require.NoError(t, err)

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "seeded hello"}},
	}})
	require.NoError(t, err)

	err = rt.recordActivity(context.Background(), testRecordBatch(&runlog.ActivityInput{
		Type:        transcript.RunLogMessagesSeeded,
		EventKey:    "evt-transcript-seed",
		RunID:       "run-1",
		AgentID:     "svc.agent",
		SessionID:   "sess-1",
		TurnID:      "turn-1",
		TimestampMS: 1,
		Payload:     payload,
	}))
	require.NoError(t, err)
	require.Len(t, rl.events, 1)
	require.Equal(t, 0, sink.count)
}

func TestRecordActivity_TranscriptDeltaStreamsCommittedAssistantTurns(t *testing.T) {
	t.Parallel()

	rl := &recordingRunlog{}
	store := sessioninmem.New()
	sink := &countingStreamSink{}
	sub, err := stream.NewSubscriber(sink)
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore:    rl,
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}
	_, err = store.CreateSession(context.Background(), "sess-1", time.Now().UTC())
	require.NoError(t, err)

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	require.NoError(t, err)

	err = rt.recordActivity(context.Background(), testRecordBatch(&runlog.ActivityInput{
		Type:        transcript.RunLogMessagesAppended,
		EventKey:    "evt-transcript-assistant",
		RunID:       "run-1",
		AgentID:     "svc.agent",
		SessionID:   "sess-1",
		TurnID:      "turn-1",
		TimestampMS: 1,
		Payload:     payload,
	}))
	require.NoError(t, err)
	require.Len(t, rl.events, 1)
	require.Equal(t, 1, sink.count)
}

func TestRecordActivity_TranscriptRetryCompletesOneCommittedAssistantTurn(t *testing.T) {
	ctx := context.Background()
	store := sessioninmem.New()
	_, err := store.CreateSession(ctx, "sess-1", time.Unix(0, 0).UTC())
	require.NoError(t, err)
	sink := &failAfterFirstKeyedPublicationSink{
		err:    errors.New("post-publication callback failed"),
		events: make(map[string]stream.Event),
	}
	sub, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	rt := &Runtime{
		RunEventStore:    runloginmem.New(),
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}
	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	require.NoError(t, err)
	input := &runlog.ActivityInput{
		Type:        transcript.RunLogMessagesAppended,
		EventKey:    "evt-transcript-retry",
		RunID:       "run-1",
		AgentID:     "svc.agent",
		SessionID:   "sess-1",
		TurnID:      "turn-1",
		TimestampMS: 1,
		Payload:     payload,
	}

	require.Error(t, rt.recordActivity(ctx, testRecordBatch(input)))
	require.NoError(t, rt.recordActivity(ctx, testRecordBatch(input)))

	page, err := rt.RunEventStore.List(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Len(t, sink.events, 1)
	require.Contains(t, sink.events, "evt-transcript-retry/assistant/0")
}

func TestRecordActivity_TranscriptDeltaStreamsCommittedAssistantCitationsTurns(t *testing.T) {
	t.Parallel()

	rl := &recordingRunlog{}
	store := sessioninmem.New()
	sink := &countingStreamSink{}
	sub, err := stream.NewSubscriber(sink)
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore:    rl,
		Bus:              hooks.NewBus(),
		SessionStore:     store,
		streamSubscriber: sub,
	}
	_, err = store.CreateSession(context.Background(), "sess-1", time.Now().UTC())
	require.NoError(t, err)

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.CitationsPart{
			Text: "supported by cited content",
		}},
	}})
	require.NoError(t, err)

	err = rt.recordActivity(context.Background(), testRecordBatch(&runlog.ActivityInput{
		Type:        transcript.RunLogMessagesAppended,
		EventKey:    "evt-transcript-assistant-citations",
		RunID:       "run-1",
		AgentID:     "svc.agent",
		SessionID:   "sess-1",
		TurnID:      "turn-1",
		TimestampMS: 1,
		Payload:     payload,
	}))
	require.NoError(t, err)
	require.Len(t, rl.events, 1)
	require.Equal(t, 1, sink.count)
}

var _ runlog.Store = (*recordingRunlog)(nil)
var _ session.Store = (*sessioninmem.Store)(nil)
