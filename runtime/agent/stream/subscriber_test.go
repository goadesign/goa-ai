package stream

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type mockSink struct {
	events []Event
	err    error
}

func (m *mockSink) Send(ctx context.Context, evt Event) error {
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, evt)
	return nil
}

func (m *mockSink) Close(ctx context.Context) error { return nil }

func TestStreamSubscriber_DoesNotStreamCompletedAssistantMessageAsLiveOutput(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewAssistantMessageEvent("r1", agent.Ident("agent1"), "session-1", "hello", nil)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Empty(t, sink.events)
}

func TestStreamSubscriber_AssistantTurnCommitted(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewAssistantTurnCommittedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"00000000-0000-4000-8000-000000000001",
		[]*model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "hello "}},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{model.CitationsPart{
					Text:      "world",
					Citations: []model.Citation{{Title: "manual"}},
				}},
				Meta: map[string]any{"source": "manual"},
			},
		},
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventAssistantTurn, sink.events[0].Type())
	v, ok := sink.events[0].(AssistantTurn)
	require.True(t, ok)
	require.Equal(t, "00000000-0000-4000-8000-000000000001", v.Data.ResponseID)
	require.Equal(t, v.Data.ResponseID, v.EventKey())
	require.Equal(t, evt.Messages, v.Data.Messages)
	require.Equal(t, "hello world", v.Data.Text())

	encoded, err := json.Marshal(v.Data)
	require.NoError(t, err)
	var decoded AssistantTurnPayload
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, v.Data, decoded)
}

func TestStreamSubscriberRejectsAssistantTurnWithMismatchedEventKey(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	evt := hooks.NewAssistantTurnCommittedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"response-1",
		[]*model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "answer"}},
		}},
	)
	evt.SetEventKey("response-2")

	err = sub.HandleEvent(t.Context(), evt)

	require.ErrorContains(t, err, "response id does not match event key")
	require.Empty(t, sink.events)
}

func TestStreamSubscriber_PreservesHookEventKey(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)

	evt := hooks.NewPlannerNoteEvent("r1", agent.Ident("agent1"), "session-1", "note", nil)
	require.NoError(t, sub.HandleEvent(context.Background(), evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, evt.EventKey(), sink.events[0].EventKey())
}

func TestStreamSubscriber_ToolStart(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewToolCallScheduledEvent("r1", agent.Ident("agent1"), "session-1", tools.Ident("svc.tool"), "call-1", rawjson.Message([]byte(`{"q":1}`)), "queue", "", 0)
	evt.DisplayHint = "custom display hint"
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolStart, sink.events[0].Type())
	start, ok := sink.events[0].(ToolStart)
	require.True(t, ok)
	require.Equal(t, "custom display hint", start.Data.DisplayHint)
}

func TestStreamSubscriber_ToolEnd_EmitsServerData(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	server := rawjson.Message([]byte(`[{"kind":"example.evidence","data":[{"uri":"example://points/123"}]}]`))
	evt := hooks.NewToolResultReceivedEvent(
		"result-run",
		agent.Ident("agent1"),
		"session-1",
		"call-run",
		tools.Ident("svc.tool"),
		"call-1",
		"",
		nil,
		server,
		"",
		nil,
		0,
		nil,
		nil,
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolEnd, sink.events[0].Type())
	end, ok := sink.events[0].(ToolEnd)
	require.True(t, ok)
	require.Equal(t, "result-run", end.RunID())
	require.Equal(t, "call-run", end.Data.CallRunID)
	require.JSONEq(t, string(server), string(end.ServerData))
}

func TestStreamSubscriber_ToolEnd_PreservesCompleteResult(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	result := rawjson.Message(`{"value":"done"}`)
	evt := hooks.NewToolResultReceivedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"r1",
		tools.Ident("svc.tool"),
		"call-1",
		"",
		result,
		nil,
		"",
		nil,
		0,
		nil,
		nil,
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolEnd, sink.events[0].Type())
	end, ok := sink.events[0].(ToolEnd)
	require.True(t, ok)
	require.JSONEq(t, string(result), string(end.Data.Result))
}

func TestStreamSubscriber_ToolEnd_RejectsMissingCallRunID(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)

	evt := hooks.NewToolResultReceivedEvent(
		"result-run",
		agent.Ident("agent1"),
		"session-1",
		"",
		tools.Ident("svc.tool"),
		"call-1",
		"",
		nil,
		nil,
		"",
		nil,
		0,
		nil,
		nil,
	)
	require.ErrorContains(t, sub.HandleEvent(context.Background(), evt), "missing call_run_id")
	require.Empty(t, sink.events)
}

func TestStreamSubscriber_ToolUpdate(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewToolCallUpdatedEvent("r1", agent.Ident("agent1"), "session-1", "parent-1", 3)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolUpdate, sink.events[0].Type())
	upd, ok := sink.events[0].(ToolUpdate)
	require.True(t, ok)
	require.Equal(t, "parent-1", upd.Data.ToolCallID)
	require.Equal(t, 3, upd.Data.ExpectedChildrenTotal)
}

func TestStreamSubscriber_PromptRendered(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	evt := hooks.NewPromptRenderedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"example.agent.system",
		"v2",
		prompt.Scope{
			SessionID: "session-1",
			Labels: map[string]string{
				"account": "acme",
				"region":  "west",
			},
		},
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	require.Equal(t, EventPromptRendered, sink.events[0].Type())
	got, ok := sink.events[0].(PromptRendered)
	require.True(t, ok)
	require.Equal(t, "example.agent.system", got.Data.PromptID)
	require.Equal(t, "v2", got.Data.Version)
	require.Equal(t, "session-1", got.Data.Scope.SessionID)
	require.Equal(t, "acme", got.Data.Scope.Labels["account"])
	require.Equal(t, "west", got.Data.Scope.Labels["region"])
}

func TestStreamSubscriber_PromptRenderedRespectsProfileToggle(t *testing.T) {
	sink := &mockSink{}
	profile := DefaultProfile()
	profile.PromptRendered = false

	sub, err := NewSubscriberWithProfile(sink, profile)
	require.NoError(t, err)
	ctx := context.Background()

	evt := hooks.NewPromptRenderedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"example.agent.system",
		"v2",
		prompt.Scope{SessionID: "session-1"},
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Empty(t, sink.events)
}

func TestStreamSubscriberModelOutputEventsRespectProfile(t *testing.T) {
	profile := DefaultProfile()
	profile.Assistant = false
	profile.Thoughts = false
	sink := &mockSink{}
	subscriber, err := NewSubscriberWithProfile(sink, profile)
	require.NoError(t, err)

	events := []Event{
		AssistantReply{
			Base: NewBase(EventAssistantReply, "run-1", "session-1", AssistantReplyPayload{}),
		},
		PlannerThought{
			Base: NewBase(EventPlannerThought, "run-1", "session-1", PlannerThoughtPayload{}),
		},
	}
	for _, event := range events {
		published, err := subscriber.HandleModelOutputEvent(t.Context(), event)
		require.NoError(t, err)
		require.False(t, published)
	}
	require.Empty(t, sink.events)
}

func TestStreamSubscriberThoughtProfilePreservesCommittedMessages(t *testing.T) {
	profile := DefaultProfile()
	profile.Thoughts = false
	sink := &mockSink{}
	subscriber, err := NewSubscriberWithProfile(sink, profile)
	require.NoError(t, err)
	message := &model.Message{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "provider reasoning"},
			model.TextPart{Text: "answer"},
		},
		Meta: map[string]any{"provider_item": "item-1"},
	}
	evt := hooks.NewAssistantTurnCommittedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"response-1",
		[]*model.Message{message},
	)

	require.NoError(t, subscriber.HandleEvent(t.Context(), evt))
	require.Len(t, sink.events, 1)
	turn, ok := sink.events[0].(AssistantTurn)
	require.True(t, ok)
	require.Equal(t, []*model.Message{message}, turn.Data.Messages)
}

func TestStreamSubscriber_WorkflowFromRunCompleted(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewRunCompletedEvent("r1", agent.Ident("agent1"), "session-1", "success", run.PhaseCompleted, nil, nil, nil)
	evt.SetEventKey("r1/12")
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 2)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, EventWorkflow, wf.Type())
	require.Equal(t, "completed", wf.Data.Phase)
	require.Equal(t, "success", wf.Data.Status)
	require.Equal(t, evt.EventKey(), wf.EventKey())
	end, ok := sink.events[1].(RunStreamEnd)
	require.True(t, ok)
	require.Equal(t, EventRunStreamEnd, end.Type())
	require.Equal(t, "r1", end.RunID())
	require.Equal(t, evt.EventKey()+"/"+string(EventRunStreamEnd), end.EventKey())
	require.NotEqual(t, wf.EventKey(), end.EventKey())

	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 4)
	require.Equal(t, sink.events[0], sink.events[2])
	require.Equal(t, sink.events[1], sink.events[3])
}

func TestStreamSubscriberWorkflowFromRunSuspended(t *testing.T) {
	sink := &mockSink{}
	subscriber, err := NewSubscriber(sink)
	require.NoError(t, err)
	event := hooks.NewRunSuspendedEvent(
		"r1", agent.Ident("agent1"), "session-1", "suspension-1", "v1", 1, nil,
	)

	require.NoError(t, subscriber.HandleEvent(t.Context(), event))
	require.Len(t, sink.events, 2)
	workflow, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, "suspended", workflow.Data.Phase)
	require.Equal(t, "suspended", workflow.Data.Status)
	_, ok = sink.events[1].(RunStreamEnd)
	require.True(t, ok)
}

func TestStreamSubscriber_WorkflowFromRunCompleted_FailedHasPublicAndDebugErrors(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	pe := model.NewProviderError("bedrock", "converse_stream", 429, model.ProviderErrorKindRateLimited, "rate_limited", "", "", true, context.DeadlineExceeded)
	evt := hooks.NewRunCompletedEvent("r1", agent.Ident("agent1"), "session-1", "failed", run.PhaseFailed, nil, pe, nil)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 2)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, "failed", wf.Data.Phase)
	require.Equal(t, "failed", wf.Data.Status)
	require.NotNil(t, wf.Data.Failure)
	require.NotEmpty(t, wf.Data.Failure.Message)
	require.NotEmpty(t, wf.Data.Failure.DebugMessage)
	require.Equal(t, "bedrock", wf.Data.Failure.Provider)
	require.Equal(t, "rate_limited", wf.Data.Failure.Kind)
	require.True(t, wf.Data.Failure.Retryable)
	require.Equal(t, EventRunStreamEnd, sink.events[1].Type())
}

func TestStreamSubscriber_WorkflowFromRunCompleted_CanceledHasNoFailureMetadata(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	evt := hooks.NewRunCompletedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"canceled",
		run.PhaseCanceled,
		nil,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonUserRequested},
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 2)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, "canceled", wf.Data.Phase)
	require.Equal(t, "canceled", wf.Data.Status)
	require.Nil(t, wf.Data.Failure)
	require.NotNil(t, wf.Data.Cancellation)
	require.Equal(t, run.CancellationReasonUserRequested, wf.Data.Cancellation.Reason)
	require.Equal(t, EventRunStreamEnd, sink.events[1].Type())
}

func TestStreamSubscriber_WorkflowFromRunPhaseChanged(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewRunPhaseChangedEvent("r1", agent.Ident("agent1"), "session-1", run.PhasePlanning)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, EventWorkflow, wf.Type())
	require.Equal(t, "planning", wf.Data.Phase)
	require.Empty(t, wf.Data.Status)
}

func TestStreamSubscriber_ThinkingBlock_StructuredFinalHasNoDelta(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	// Structured provider reasoning block with final=true and signature populated.
	evt := hooks.NewThinkingBlockEvent("run-1", agent.Ident("agent-1"), "session-1", "full reasoning text", "sig-123", nil, 0, true)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	th, ok := sink.events[0].(PlannerThought)
	require.True(t, ok)
	require.Equal(t, EventPlannerThought, th.Type())
	// Full text and signature preserved for replay.
	require.Equal(t, "full reasoning text", th.Data.Text)
	require.Equal(t, "sig-123", th.Data.Signature)
	require.True(t, th.Data.Final)
	// Note (streaming delta) must be empty so UIs do not append the full text again.
	require.Empty(t, th.Data.Note)
}

func TestStreamSubscriber_ThinkingBlock_StructuredNonFinalDelta(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	// Non-final unsigned block should be treated as a delta.
	evt := hooks.NewThinkingBlockEvent("run-1", agent.Ident("agent-1"), "session-1", "partial", "", nil, 0, false)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	th, ok := sink.events[0].(PlannerThought)
	require.True(t, ok)
	require.Equal(t, EventPlannerThought, th.Type())
	require.Equal(t, "partial", th.Data.Text)
	require.False(t, th.Data.Final)
	// Delta propagated via Note for streaming.
	require.Equal(t, "partial", th.Data.Note)
}

func TestStreamSubscriber_ChildRunLinked(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	evt := hooks.NewChildRunLinkedEvent(
		"parent-run",
		agent.Ident("parent.agent"),
		"session-1",
		tools.Ident("svc.agent.records"),
		"parent-call",
		"child-run",
		agent.Ident("child.agent"),
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	ar, ok := sink.events[0].(ChildRunLinked)
	require.True(t, ok)
	require.Equal(t, EventChildRunLinked, ar.Type())
	require.Equal(t, "parent-run", ar.RunID())
	require.Equal(t, "svc.agent.records", ar.Data.ToolName)
	require.Equal(t, "parent-call", ar.Data.ToolCallID)
	require.Equal(t, "child-run", ar.Data.ChildRunID)
	require.Equal(t, agent.Ident("child.agent"), ar.Data.ChildAgentID)
}

func TestStreamSubscriber_MultipleRunsPreserveRunID(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	// Parent and child runs must retain distinct RunIDs in the stream. The
	// subscriber never rewrites run identities; higher-level projections that
	// want a flattened view are expected to build it on top.
	parent := hooks.NewRunPhaseChangedEvent("parent-run", agent.Ident("parent.agent"), "session-1", run.PhasePlanning)
	child := hooks.NewRunPhaseChangedEvent("child-run", agent.Ident("child.agent"), "session-1", run.PhasePlanning)

	require.NoError(t, sub.HandleEvent(ctx, parent))
	require.NoError(t, sub.HandleEvent(ctx, child))

	require.Len(t, sink.events, 2)
	require.Equal(t, "parent-run", sink.events[0].RunID())
	require.Equal(t, "child-run", sink.events[1].RunID())
}

func TestStreamSubscriber_ToolEndPrecedesRunStreamEnd(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	toolEnd := hooks.NewToolResultReceivedEvent(
		"r1",
		agent.Ident("agent1"),
		"session-1",
		"r1",
		tools.Ident("svc.tool"),
		"call-1",
		"",
		rawjson.Message([]byte(`{"ok":true}`)),
		nil,
		"",
		nil,
		0,
		nil,
		nil,
	)
	require.NoError(t, sub.HandleEvent(ctx, toolEnd))

	completed := hooks.NewRunCompletedEvent("r1", agent.Ident("agent1"), "session-1", "success", run.PhaseCompleted, nil, nil, nil)
	require.NoError(t, sub.HandleEvent(ctx, completed))

	require.Len(t, sink.events, 3)
	require.Equal(t, EventToolEnd, sink.events[0].Type())
	require.Equal(t, EventWorkflow, sink.events[1].Type())
	require.Equal(t, EventRunStreamEnd, sink.events[2].Type())
}
