package runtime

// model_output_stream_test.go fixes the real-time boundary between model chunks
// and durable runtime records. Text and thoughts reach the session stream
// immediately, while partial tool JSON stays out of the run log.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/stream"
)

func TestModelOutputStreamsTextAndThoughtsImmediately(t *testing.T) {
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, sink),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
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
			Name:  "catalog.lookup",
			Delta: `{"partial":`,
		},
	}))

	events := sink.snapshot()
	require.Len(t, events, 2)
	thought, ok := events[0].(stream.PlannerThought)
	require.True(t, ok)
	require.Equal(t, "checking readings", thought.Data.Note)
	require.Equal(t, "response-1", thought.Data.ResponseID)
	reply, ok := events[1].(stream.AssistantReply)
	require.True(t, ok)
	require.Equal(t, "The freezer is stable.", reply.Data.Text)
	require.Equal(t, "response-1", reply.Data.ResponseID)
}

func TestThousandsOfModelFragmentsStreamWithoutLifecycleEvents(t *testing.T) {
	const fragmentCount = 2_500
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, sink),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
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
	require.Len(t, events, fragmentCount)
	for _, event := range events {
		require.IsType(t, stream.AssistantReply{}, event)
	}
}

func TestCanceledPlannerExecutionCannotStreamModelOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, sink),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	err := journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "late fragment"}},
		},
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, sink.snapshot())
}

func TestModelOutputDoesNotStreamProbeInvocations(t *testing.T) {
	ctx := context.Background()
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, sink),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
	probe := mustBeginModelInvocation(t, journal)
	designated := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(designated))

	require.NoError(t, journal.recordModelChunk(ctx, probe, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "private probe text"}},
		},
	}))
	require.Empty(t, sink.snapshot())

	require.NoError(t, journal.recordModelChunk(ctx, designated, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "visible planner text"}},
		},
	}))
	events := sink.snapshot()
	require.Len(t, events, 1)
	reply, ok := events[0].(stream.AssistantReply)
	require.True(t, ok)
	require.Equal(t, "visible planner text", reply.Data.Text)
}

func TestPlannerActivityExecutionOwnsResponseID(t *testing.T) {
	rt := newTestRuntimeWithPlanner("svc.agent", &stubPlanner{})
	sink := &recordingStreamSink{}
	rt.streamSubscriber = runtimeWithModelOutputSink(t, sink).streamSubscriber
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

	require.Equal(t, first.publicationBatchID, first.invocations.responseID)
	require.Equal(t, second.publicationBatchID, second.invocations.responseID)
	require.NotEqual(t, first.invocations.responseID, second.invocations.responseID)
	require.Empty(t, sink.snapshot())
}

func runtimeWithModelOutputSink(t *testing.T, sink stream.Sink) *Runtime {
	t.Helper()
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	return &Runtime{streamSubscriber: subscriber}
}
