package stream

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
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

func TestStreamSubscriber(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewAssistantMessageEvent("r1", agent.Ident("agent1"), "", "hello", nil)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventAssistantReply, sink.events[0].Type())
	v, ok := sink.events[0].(AssistantReply)
	require.True(t, ok)
	require.Equal(t, "hello", v.Data.Text)
}

func TestStreamSubscriber_ToolStart(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewToolCallScheduledEvent("r1", agent.Ident("agent1"), "", tools.Ident("svc.tool"), "call-1", json.RawMessage(`{"q":1}`), "queue", "", 0)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolStart, sink.events[0].Type())
	_, ok := sink.events[0].(ToolStart)
	require.True(t, ok)
}

func TestStreamSubscriber_ToolUpdate(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewToolCallUpdatedEvent("r1", agent.Ident("agent1"), "", "parent-1", 3)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	require.Equal(t, EventToolUpdate, sink.events[0].Type())
	upd, ok := sink.events[0].(ToolUpdate)
	require.True(t, ok)
	require.Equal(t, "parent-1", upd.Data.ToolCallID)
	require.Equal(t, 3, upd.Data.ExpectedChildrenTotal)
}

func TestStreamSubscriber_WorkflowFromRunCompleted(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewRunCompletedEvent("r1", agent.Ident("agent1"), "", "success", run.PhaseCompleted, nil)
	require.NoError(t, sub.HandleEvent(ctx, evt))
	require.Len(t, sink.events, 1)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, EventWorkflow, wf.Type())
	require.Equal(t, "completed", wf.Data.Phase)
	require.Equal(t, "success", wf.Data.Status)
}

func TestStreamSubscriber_WorkflowFromRunCompleted_FailedHasPublicAndDebugErrors(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	pe := model.NewProviderError("bedrock", "converse_stream", 429, model.ProviderErrorKindRateLimited, "rate_limited", "", "", true, context.DeadlineExceeded)
	evt := hooks.NewRunCompletedEvent("r1", agent.Ident("agent1"), "", "failed", run.PhaseFailed, pe)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, "failed", wf.Data.Phase)
	require.Equal(t, "failed", wf.Data.Status)
	require.NotEmpty(t, wf.Data.Error)
	require.NotEmpty(t, wf.Data.DebugError)
	require.Equal(t, "bedrock", wf.Data.ErrorProvider)
	require.Equal(t, "rate_limited", wf.Data.ErrorKind)
	require.True(t, wf.Data.Retryable)
}

func TestStreamSubscriber_WorkflowFromRunCompleted_CanceledHasNoError(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	evt := hooks.NewRunCompletedEvent("r1", agent.Ident("agent1"), "", "canceled", run.PhaseCanceled, context.Canceled)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	wf, ok := sink.events[0].(Workflow)
	require.True(t, ok)
	require.Equal(t, "canceled", wf.Data.Phase)
	require.Equal(t, "canceled", wf.Data.Status)
	require.Empty(t, wf.Data.Error)
	require.NotEmpty(t, wf.Data.DebugError)
}

func TestStreamSubscriber_WorkflowFromRunPhaseChanged(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()
	evt := hooks.NewRunPhaseChangedEvent("r1", agent.Ident("agent1"), "", run.PhasePlanning)
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
	evt := hooks.NewThinkingBlockEvent("run-1", agent.Ident("agent-1"), "", "full reasoning text", "sig-123", nil, 0, true)
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
	evt := hooks.NewThinkingBlockEvent("run-1", agent.Ident("agent-1"), "", "partial", "", nil, 0, false)
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

func TestStreamSubscriber_AgentRunStarted(t *testing.T) {
	sink := &mockSink{}
	sub, err := NewSubscriber(sink)
	require.NoError(t, err)
	ctx := context.Background()

	evt := hooks.NewAgentRunStartedEvent(
		"parent-run",
		agent.Ident("parent.agent"),
		"session-1",
		tools.Ident("svc.agent.ada"),
		"parent-call",
		"child-run",
		agent.Ident("child.agent"),
	)
	require.NoError(t, sub.HandleEvent(ctx, evt))

	require.Len(t, sink.events, 1)
	ar, ok := sink.events[0].(AgentRunStarted)
	require.True(t, ok)
	require.Equal(t, EventAgentRunStarted, ar.Type())
	require.Equal(t, "parent-run", ar.RunID())
	require.Equal(t, "svc.agent.ada", ar.Data.ToolName)
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
	parent := hooks.NewRunPhaseChangedEvent("parent-run", agent.Ident("parent.agent"), "", run.PhasePlanning)
	child := hooks.NewRunPhaseChangedEvent("child-run", agent.Ident("child.agent"), "", run.PhasePlanning)

	require.NoError(t, sub.HandleEvent(ctx, parent))
	require.NoError(t, sub.HandleEvent(ctx, child))

	require.Len(t, sink.events, 2)
	require.Equal(t, "parent-run", sink.events[0].RunID())
	require.Equal(t, "child-run", sink.events[1].RunID())
}
