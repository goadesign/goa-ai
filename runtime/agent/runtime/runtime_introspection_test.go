package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestIntrospectionListsAgentsAndToolSpecs(t *testing.T) {
	rt := New()

	// Register an agent with one tool spec
	spec := tools.ToolSpec{Name: "svc.ts.tool", Toolset: "svc.ts"}
	reg := AgentRegistration{
		ID:                  "svc.agent",
		Planner:             &stubPlanner{},
		Workflow:            engine.WorkflowDefinition{Name: "wf", TaskQueue: "q", Handler: func(engine.WorkflowContext, *RunInput) (*RunOutput, error) { return nil, nil }},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
		Specs:               []tools.ToolSpec{spec},
	}
	require.NoError(t, rt.RegisterAgent(context.Background(), reg))

	// ListAgents
	gotAgents := rt.ListAgents()
	require.Equal(t, []agent.Ident{"svc.agent"}, gotAgents)

	// ToolSpec
	gotSpec, ok := rt.ToolSpec(tools.Ident("svc.ts.tool"))
	require.True(t, ok)
	require.Equal(t, spec.Name, gotSpec.Name)

	// ToolSpecsForAgent
	specs := rt.ToolSpecsForAgent(agent.Ident("svc.agent"))
	require.Len(t, specs, 1)
	require.Equal(t, spec.Name, specs[0].Name)
}

type recordingSink struct{ events []stream.Event }

func (s *recordingSink) Send(ctx context.Context, e stream.Event) error {
	s.events = append(s.events, e)
	return nil
}
func (s *recordingSink) Close(ctx context.Context) error { return nil }

func TestSubscribeRunFiltersByRunID(t *testing.T) {
	rt := New()
	sink := &recordingSink{}
	stop, err := rt.SubscribeRun(context.Background(), "run-1", sink)
	require.NoError(t, err)
	defer stop()

	// Publish events for two runs; only run-1 should be forwarded
	require.NoError(t, rt.Bus.Publish(context.Background(), hooks.NewAssistantMessageEvent("run-1", "agent", "", "hi", nil)))
	require.NoError(t, rt.Bus.Publish(context.Background(), hooks.NewAssistantMessageEvent("run-2", "agent", "", "skip", nil)))

	require.Len(t, sink.events, 1)
	require.Equal(t, "run-1", sink.events[0].RunID())
}
