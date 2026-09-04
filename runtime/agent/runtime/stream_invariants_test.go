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
	"goa.design/goa-ai/runtime/agent/rawjson"
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
	rt := New(newTestStore(),
		WithEngine(engineinmem.New()),
		WithHooks(bus),
		WithStream(sink, stream.RuntimeHostProfile()),
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
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: testRegistrationDefinition("child.agent",

		engine.WorkflowDefinition{
			Name:    "child.workflow",
			Handler: rt.ExecuteWorkflow,
		}, nil),

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:    "child.workflow",
			Handler: rt.ExecuteWorkflow,
		}).Handler, Planner: childPlanner,

		PlanActivityName:    "child.plan",
		ResumeActivityName:  "child.resume",
		ExecuteToolActivity: "child.execute_tool",
	}))

	const (
		parentRunID  = "run-parent"
		sessionID    = "session-1"
		turnID       = "turn-1"
		invokeToolID = tools.Ident("invoke")
		toolsetName  = "svc.agenttools"
	)

	sess, err := createSessionForTest(ctx, rt.Store, sessionID)
	require.NoError(t, err)
	require.Equal(t, sessionID, sess.ID)
	require.Equal(t, session.StatusActive, sess.Status)

	agentTools := NewAgentToolsetRegistration(rt, AgentToolConfig{
		Definition: testAgentDefinition(agent.Ident("child.agent"), "child.workflow", "default", nil, nil),
		Name:       toolsetName,
		AgentToolContent: AgentToolContent{
			Prompt: func(id tools.Ident, payload any) string {
				return "invoke"
			},
		},
	})
	agentTools.Specs = []tools.ToolSpec{
		newAnyJSONSpec(invokeToolID),
	}
	agentTools.Specs[0].IsAgentTool = true
	agentTools.Specs[0].AgentID = "child.agent"
	require.NoError(t, rt.RegisterToolset(agentTools))

	parentPlanner := &stubPlanner{
		start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				ToolCalls: []planner.ToolRequest{
					{
						Name:    invokeToolID,
						Payload: rawjson.Message(`{}`),
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
	parentDefinition := NewAgentDefinition(
		AgentRoute{
			ID:               "parent.agent",
			WorkflowName:     "parent.workflow",
			DefaultTaskQueue: "test",
		},
		agentTools.Specs,
		nil,
		nil,
		[]tools.Ident{invokeToolID},
		nil,
	)
	require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: parentDefinition,

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:    "parent.workflow",
			Handler: rt.ExecuteWorkflow,
		}).Handler, Planner: parentPlanner,

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
	invokeCallID := generateDeterministicToolCallID(parentRunID, turnID, 1, invokeToolID, 0)
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
