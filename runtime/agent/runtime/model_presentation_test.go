package runtime

// model_presentation_test.go fixes the real-time boundary between validated
// model chunks and durable runtime records. Text and thoughts must reach the
// session stream immediately, while partial tool JSON and all provisional
// presentation events must stay out of the run log.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/stream"
)

type failOnceDiscardSink struct {
	mu     sync.Mutex
	failed bool
	events []stream.Event
	err    error
}

func (s *failOnceDiscardSink) Send(_ context.Context, event stream.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if presentation, ok := event.(stream.ModelPresentation); ok &&
		presentation.Data.State == stream.ModelPresentationDiscarded &&
		!s.failed {
		s.failed = true
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *failOnceDiscardSink) Close(context.Context) error {
	return nil
}

func (s *failOnceDiscardSink) snapshot() []stream.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stream.Event(nil), s.events...)
}

func TestModelPresentationStreamsTextAndThoughtsImmediately(t *testing.T) {
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:        runtimeWithPresentationSink(t, sink),
		runID:          "run-1",
		sessionID:      "session-1",
		presentationID: "presentation-1",
	}
	require.NoError(t, journal.startPresentation(ctx))
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.ThinkingChunk{
		Message: model.Message{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ThinkingPart{
				Text:  "checking readings",
				Final: true,
			}},
		},
	}))
	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "The freezer is stable."}},
		},
	}))
	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.ToolCallDeltaChunk{
		Delta: model.ToolCallDelta{
			ID:    "call-1",
			Name:  "atlas.read",
			Delta: `{"partial":`,
		},
	}))

	events := sink.snapshot()
	require.Len(t, events, 3)
	started, ok := events[0].(stream.ModelPresentation)
	require.True(t, ok)
	require.Equal(t, stream.ModelPresentationStarted, started.Data.State)
	require.Equal(t, "presentation-1", started.Data.PresentationID)
	thought, ok := events[1].(stream.PlannerThought)
	require.True(t, ok)
	require.Equal(t, "checking readings", thought.Data.Note)
	require.Equal(t, "presentation-1", thought.Data.PresentationID)
	reply, ok := events[2].(stream.AssistantReply)
	require.True(t, ok)
	require.Equal(t, "The freezer is stable.", reply.Data.Text)
	require.Equal(t, "presentation-1", reply.Data.PresentationID)
}

func TestThousandsOfModelFragmentsStayOnTheProvisionalStream(t *testing.T) {
	const fragmentCount = 2_500
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:        runtimeWithPresentationSink(t, sink),
		runID:          "run-1",
		sessionID:      "session-1",
		presentationID: "presentation-1",
	}
	require.NoError(t, journal.startPresentation(ctx))
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	for index := range fragmentCount {
		require.NoError(t, journal.recordModelChunk(ctx, invocation, model.TextChunk{
			Message: model.Message{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{
					Text: "fragment",
				}},
			},
		}), "fragment %d", index)
	}

	events := sink.snapshot()
	require.Len(t, events, fragmentCount+1)
	require.IsType(t, stream.ModelPresentation{}, events[0])
	for _, event := range events[1:] {
		require.IsType(t, stream.AssistantReply{}, event)
	}
}

func TestCanceledPlannerExecutionCannotStartPresentation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:        runtimeWithPresentationSink(t, sink),
		runID:          "run-1",
		sessionID:      "session-1",
		presentationID: "presentation-1",
	}
	require.NoError(t, journal.startPresentation(context.Background()))
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	err := journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "late fragment"}},
		},
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, sink.snapshot(), 1)
}

func TestModelPresentationDoesNotStreamProbeInvocations(t *testing.T) {
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:        runtimeWithPresentationSink(t, sink),
		runID:          "run-1",
		sessionID:      "session-1",
		presentationID: "presentation-1",
	}
	require.NoError(t, journal.startPresentation(ctx))
	probe := mustBeginModelInvocation(t, journal)
	designated := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(designated))

	require.NoError(t, journal.recordModelChunk(ctx, probe, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "private probe text"}},
		},
	}))
	require.Len(t, sink.snapshot(), 1)

	require.NoError(t, journal.recordModelChunk(ctx, designated, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "visible planner text"}},
		},
	}))
	events := sink.snapshot()
	require.Len(t, events, 2)
	require.IsType(t, stream.ModelPresentation{}, events[0])
	reply, ok := events[1].(stream.AssistantReply)
	require.True(t, ok)
	require.Equal(t, "visible planner text", reply.Data.Text)
}

func TestModelPresentationDiscardsRejectedOutput(t *testing.T) {
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:        runtimeWithPresentationSink(t, sink),
		runID:          "run-1",
		sessionID:      "session-1",
		presentationID: "presentation-1",
	}
	require.NoError(t, journal.startPresentation(ctx))
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))
	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "unaccepted answer"}},
		},
	}))

	rejected := errors.New("model output rejected")
	require.NoError(t, journal.finishModelInvocation(ctx, invocation, rejected))

	events := sink.snapshot()
	require.Len(t, events, 3)
	discarded, ok := events[2].(stream.ModelPresentation)
	require.True(t, ok)
	require.Equal(t, stream.ModelPresentationDiscarded, discarded.Data.State)
	require.Equal(t, "presentation-1", discarded.Data.PresentationID)
}

func TestModelPresentationRetriesFailedDiscard(t *testing.T) {
	ctx := context.Background()
	streamErr := errors.New("stream unavailable")
	sink := &failOnceDiscardSink{err: streamErr}
	journal := &modelInvocationJournal{
		runtime:        runtimeWithPresentationSink(t, sink),
		runID:          "run-1",
		sessionID:      "session-1",
		presentationID: "presentation-1",
	}
	require.NoError(t, journal.startPresentation(ctx))
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))
	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "unaccepted answer"}},
		},
	}))

	err := journal.finishModelInvocation(ctx, invocation, errors.New("model output rejected"))
	require.ErrorIs(t, err, streamErr)
	require.False(t, journal.presentationFinalized)

	require.NoError(t, journal.discardPresentations(ctx))
	require.True(t, journal.presentationFinalized)
	events := sink.snapshot()
	require.Len(t, events, 3)
	discarded := events[2].(stream.ModelPresentation)
	require.Equal(t, stream.ModelPresentationDiscarded, discarded.Data.State)
}

func TestPlannerActivityExecutionOwnsPresentationID(t *testing.T) {
	rt := newTestRuntimeWithPlanner("svc.agent", &stubPlanner{})
	sink := &recordingStreamSink{}
	rt.streamSubscriber = runtimeWithPresentationSink(t, sink).streamSubscriber
	input := &PlanActivityInput{
		AgentID: "svc.agent",
		RunID:   "run-1",
		RunContext: run.Context{
			SessionID: "session-1",
		},
	}

	first, err := rt.preparePlannerActivity(context.Background(), input, nil, nil)
	require.NoError(t, err)
	second, err := rt.preparePlannerActivity(context.Background(), input, nil, nil)
	require.NoError(t, err)

	require.Equal(t, first.publicationBatchID, first.invocations.presentationID)
	require.Equal(t, second.publicationBatchID, second.invocations.presentationID)
	require.NotEqual(t, first.invocations.presentationID, second.invocations.presentationID)
	events := sink.snapshot()
	require.Len(t, events, 2)
	firstStarted := events[0].(stream.ModelPresentation)
	secondStarted := events[1].(stream.ModelPresentation)
	require.Equal(t, first.invocations.presentationID, firstStarted.Data.PresentationID)
	require.Equal(t, second.invocations.presentationID, secondStarted.Data.PresentationID)
}

func runtimeWithPresentationSink(t *testing.T, sink stream.Sink) *Runtime {
	t.Helper()
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	return &Runtime{streamSubscriber: subscriber}
}
