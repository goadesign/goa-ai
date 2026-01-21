package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type recordingStreamSink struct {
	mu     sync.Mutex
	events []stream.Event
}

func (s *recordingStreamSink) Send(ctx context.Context, event stream.Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *recordingStreamSink) Close(ctx context.Context) error { return nil }

func (s *recordingStreamSink) snapshot() []stream.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stream.Event, len(s.events))
	copy(out, s.events)
	return out
}

func TestRunStreamEnd_ParentAfterChild(t *testing.T) {
	ctx := context.Background()
	bus := hooks.NewBus()
	sink := &recordingStreamSink{}
	rt := New(
		WithEngine(engineinmem.New()),
		WithHooks(bus),
		WithStream(sink),
		WithRunEventStore(runloginmem.New()),
		WithLogger(telemetry.NoopLogger{}),
		WithMetrics(telemetry.NoopMetrics{}),
		WithTracer(telemetry.NoopTracer{}),
	)

	childPlanner := &stubPlanner{
		start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role: "assistant",
						Parts: []model.Part{
							model.TextPart{Text: "ok"},
						},
					},
				},
			}, nil
		},
	}
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		ID:      "child.agent",
		Planner: childPlanner,
		Workflow: engine.WorkflowDefinition{
			Name:    "child.workflow",
			Handler: rt.ExecuteWorkflow,
		},
		PlanActivityName:    "child.plan",
		ResumeActivityName:  "child.resume",
		ExecuteToolActivity: "child.execute_tool",
	}))

	const (
		parentRunID  = "run-parent"
		sessionID    = "session-1"
		turnID       = "turn-1"
		invokeToolID = tools.Ident("invoke")
		invokeCallID = "invoke-1"
		toolsetName  = "svc.agenttools"
	)

	sess, err := rt.CreateSession(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, sessionID, sess.ID)
	require.Equal(t, session.StatusActive, sess.Status)

	agentTools := NewAgentToolsetRegistration(rt, AgentToolConfig{
		AgentID: "child.agent",
		Route: AgentRoute{
			ID:               agent.Ident("child.agent"),
			WorkflowName:     "child.workflow",
			DefaultTaskQueue: "default",
		},
		Name:     toolsetName,
		JSONOnly: true,
	})
	require.NoError(t, rt.RegisterToolset(agentTools))
	rt.mu.Lock()
	rt.toolSpecs[invokeToolID] = newAnyJSONSpec(invokeToolID, toolsetName)
	rt.mu.Unlock()

	parentPlanner := &stubPlanner{
		start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				ToolCalls: []planner.ToolRequest{
					{
						Name:       invokeToolID,
						ToolCallID: invokeCallID,
					},
				},
			}, nil
		},
		resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role: "assistant",
						Parts: []model.Part{
							model.TextPart{Text: "done"},
						},
					},
				},
			}, nil
		},
	}
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{
		ID:      "parent.agent",
		Planner: parentPlanner,
		Workflow: engine.WorkflowDefinition{
			Name:    "parent.workflow",
			Handler: rt.ExecuteWorkflow,
		},
		PlanActivityName:    "parent.plan",
		ResumeActivityName:  "parent.resume",
		ExecuteToolActivity: "parent.execute_tool",
	}))

	client := rt.MustClient(agent.Ident("parent.agent"))
	_, err = client.Run(
		ctx,
		sessionID,
		[]*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hi"},
				},
			},
		},
		WithRunID(parentRunID),
		WithTurnID(turnID),
	)
	require.NoError(t, err)

	events := sink.snapshot()
	childRunID := NestedRunIDForToolCall(parentRunID, invokeToolID, invokeCallID)

	childIdx := indexRunStreamEnd(events, childRunID)
	require.NotEqual(t, -1, childIdx, "expected child run_stream_end to be emitted")
	parentIdx := indexRunStreamEnd(events, parentRunID)
	require.NotEqual(t, -1, parentIdx, "expected parent run_stream_end to be emitted")

	require.Less(t, childIdx, parentIdx, "child run_stream_end must precede parent run_stream_end")
}

func indexRunStreamEnd(events []stream.Event, runID string) int {
	for i, e := range events {
		if e.RunID() != runID {
			continue
		}
		if e.Type() == stream.EventRunStreamEnd {
			return i
		}
	}
	return -1
}
