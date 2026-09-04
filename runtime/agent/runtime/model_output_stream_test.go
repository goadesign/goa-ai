package runtime

// model_output_stream_test.go verifies how explicit stream profiles route live
// model output. Text and allowed diagnostic thoughts reach the configured sink
// immediately, while partial tool JSON stays out of the run log.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/internal/modelcall"
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

func TestRuntimeUsesConfiguredStreamProfile(t *testing.T) {
	sink := &recordingStreamSink{}
	runtime := New(newTestStore(), WithStream(sink, stream.RuntimeHostProfile()))
	journal := &modelInvocationJournal{
		runtime:    runtime,
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	require.NoError(t, journal.recordModelChunk(t.Context(), invocation, model.ThinkingChunk{
		Message: model.Message{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ThinkingPart{
				Text:  "private provider reasoning",
				Final: true,
			}},
		},
	}))
	require.NoError(t, journal.recordModelChunk(t.Context(), invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "The freezer is stable."}},
		},
	}))

	events := sink.snapshot()
	require.Len(t, events, 1)
	reply, ok := events[0].(stream.AssistantReply)
	require.True(t, ok)
	require.Equal(t, "The freezer is stable.", reply.Data.Text)
}

func TestWithStreamRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		sink    stream.Sink
		profile stream.StreamProfile
		message string
	}{
		{
			name:    "missing sink",
			profile: stream.RuntimeHostProfile(),
			message: "runtime: stream sink is required",
		},
		{
			name:    "empty profile",
			sink:    &recordingStreamSink{},
			message: "runtime: stream profile must enable at least one event",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.PanicsWithValue(t, test.message, func() {
				WithStream(test.sink, test.profile)
			})
		})
	}
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

func TestModelOutputRejectsTextThatCannotCrossActivityBoundary(t *testing.T) {
	sink := &recordingStreamSink{}
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, sink),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
	journal.publishedText.WriteString(string(make([]byte, maxPublishedAssistantTextBytes)))
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	err := journal.recordModelChunk(context.Background(), invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "x"}},
		},
	})

	require.Error(t, err)
	require.Empty(t, sink.snapshot())
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

func TestDisabledAssistantStreamDoesNotRecordTextAsPublished(t *testing.T) {
	sink := &recordingStreamSink{}
	profile := stream.AgentDebugProfile()
	profile.Assistant = false
	subscriber, err := stream.NewSubscriber(sink, profile)
	require.NoError(t, err)
	journal := &modelInvocationJournal{
		runtime:    &Runtime{streamSubscriber: subscriber},
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: "response-1",
	}
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))

	require.NoError(t, journal.recordModelChunk(t.Context(), invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "hidden text"}},
		},
	}))

	require.Empty(t, journal.publishedAssistantText())
	require.Empty(t, sink.snapshot())
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

func TestAcceptedResponseMustExtendPublishedText(t *testing.T) {
	ctx := context.Background()
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, &recordingStreamSink{}),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: testPublicationBatchID,
	}
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))
	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "visible text"}},
		},
	}))
	activity := &plannerActivityInvocation{
		publicationBatchID: testPublicationBatchID,
		invocations:        journal,
		events:             newPlannerEvents("svc.agent", "run-1", "session-1"),
	}

	_, err := activity.acceptedOutput(ctx, nil, []*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "different text"}},
	}})

	require.ErrorContains(t, errors.Unwrap(err), "does not extend its published assistant text")
}

func TestFailureAfterPublishedTextReturnsTextForDurableCommit(t *testing.T) {
	ctx := context.Background()
	journal := &modelInvocationJournal{
		runtime:    runtimeWithModelOutputSink(t, &recordingStreamSink{}),
		runID:      "run-1",
		sessionID:  "session-1",
		responseID: testPublicationBatchID,
	}
	invocation := mustBeginModelInvocation(t, journal)
	require.NoError(t, journal.designateModelInvocation(invocation))
	require.NoError(t, journal.recordModelChunk(ctx, invocation, model.TextChunk{
		Message: model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "visible text"}},
		},
	}))
	transportErr := model.NewProviderError(
		"bedrock",
		"converse_stream",
		503,
		model.ProviderErrorKindUnavailable,
		"service_unavailable",
		"connection lost",
		"request-1",
		true,
		errors.New("connection lost"),
	)
	require.NoError(t, journal.finalizeModelInvocation(invocation, modelcall.Outcome{
		ProviderCall: modelcall.Result{Called: true, Err: transportErr},
	}))
	activity := &plannerActivityInvocation{
		publicationBatchID: testPublicationBatchID,
		invocations:        journal,
		events:             newPlannerEvents("svc.agent", "run-1", "session-1"),
	}

	output, err := activity.failureOutput(ctx, activity.planningError(transportErr))

	require.NoError(t, err)
	require.Equal(t, "visible text", output.PublishedAssistantText)
	require.Nil(t, output.OutputContractFailure)
	require.NotNil(t, output.PlanningFailure)
	require.Equal(t, "bedrock", output.PlanningFailure.Provider)
	require.Equal(t, string(model.ProviderErrorKindUnavailable), output.PlanningFailure.Kind)
	require.True(t, output.PlanningFailure.Retryable)
	require.Contains(t, output.PlanningFailure.DebugMessage, "reason_sha256=")
}

func runtimeWithModelOutputSink(t *testing.T, sink stream.Sink) *Runtime {
	t.Helper()
	subscriber, err := stream.NewSubscriber(sink, stream.AgentDebugProfile())
	require.NoError(t, err)
	return &Runtime{streamSubscriber: subscriber}
}
