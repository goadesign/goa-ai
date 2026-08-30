package runtime

// These tests verify that the session state returned by the durable append
// controls live delivery without a second session lookup.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type failingStreamSink struct {
	err error
}

// retryingStreamSink fails its first delivery and accepts the exact retry.
type retryingStreamSink struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (s failingStreamSink) Send(context.Context, stream.Event) error {
	return s.err
}

func (s failingStreamSink) Close(context.Context) error {
	return nil
}

func (s *retryingStreamSink) Send(context.Context, stream.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		return s.err
	}
	return nil
}

func (s *retryingStreamSink) Close(context.Context) error {
	return nil
}

// callCount returns the number of attempted stream deliveries.
func (s *retryingStreamSink) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestHookActivityUsesCommittedSessionStateForStreaming(t *testing.T) {
	streamErr := errors.New("stream send failed")
	for _, test := range []struct {
		name      string
		end       bool
		wantError bool
	}{
		{name: "active", wantError: true},
		{name: "ended", end: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore()
			_, err := store.CreateSession(ctx, "session", time.Now().UTC())
			require.NoError(t, err)
			admitRunForTest(t, store, session.RunMeta{
				AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
			})
			if test.end {
				_, err = store.EndSession(ctx, "session", time.Now().UTC())
				require.NoError(t, err)
			}
			subscriber, err := stream.NewSubscriber(failingStreamSink{err: streamErr})
			require.NoError(t, err)
			runtime := &Runtime{Store: store, Bus: hooks.NewBus(), streamSubscriber: subscriber}
			record, err := hooks.EncodeToRecordInput(
				hooks.NewPlannerNoteEvent("run", "svc.agent", "session", "note", nil),
				hooks.EncodeOptions{EventKey: "note", TimestampMS: 1},
			)
			require.NoError(t, err)

			err = runtime.recordActivity(ctx, testRecordBatch(record))
			if test.wantError {
				require.ErrorIs(t, err, streamErr)
			} else {
				require.NoError(t, err)
			}
			page, listErr := store.ListRunRecords(ctx, "run", "", 10)
			require.NoError(t, listErr)
			require.Len(t, page.Events, 2)
		})
	}
}

func TestHookActivityRetriesStreamAfterDurableInsert(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	_, err := store.CreateSession(ctx, "session", time.Now().UTC())
	require.NoError(t, err)
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})

	bus := hooks.NewBus()
	busCalls := 0
	_, err = bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	streamErr := errors.New("stream send failed")
	sink := &retryingStreamSink{err: streamErr}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{Store: store, Bus: bus, streamSubscriber: subscriber}
	record, err := hooks.EncodeToRecordInput(
		hooks.NewPlannerNoteEvent("run", "svc.agent", "session", "note", nil),
		hooks.EncodeOptions{EventKey: "note", TimestampMS: 1},
	)
	require.NoError(t, err)

	err = runtime.recordActivity(ctx, testRecordBatch(record))
	require.ErrorIs(t, err, streamErr)
	require.NoError(t, runtime.recordActivity(ctx, testRecordBatch(record)))

	require.Equal(t, 2, sink.callCount())
	require.Equal(t, 1, busCalls)
	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
}

func TestTranscriptActivityRetriesStreamAfterDurableInsert(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	_, err := store.CreateSession(ctx, "session", time.Now().UTC())
	require.NoError(t, err)
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "done"}},
	}})
	require.NoError(t, err)
	record := &RecordActivityInput{
		Type: transcript.RunLogMessagesAppended, EventKey: "messages", RunID: "run",
		AgentID: "svc.agent", SessionID: "session", TurnID: "turn", TimestampMS: 1,
		Payload: payload,
	}
	streamErr := errors.New("stream send failed")
	sink := &retryingStreamSink{err: streamErr}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{Store: store, Bus: hooks.NewBus(), streamSubscriber: subscriber}

	err = runtime.recordActivity(ctx, testRecordBatch(record))
	require.ErrorIs(t, err, streamErr)
	require.NoError(t, runtime.recordActivity(ctx, testRecordBatch(record)))

	require.Equal(t, 2, sink.callCount())
	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
}
