// Agent registration installs child preparation on each workflow queue.
// These tests check that each Agent receives its own preparation name, including
// Agents on different queues and Agents sharing one queue.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

type preparationQueueEngine struct {
	*stubEngine
	queues []string
	names  []string
}

func TestRegisterAgentPreparesChildrenOnEachWorkflowQueue(t *testing.T) {
	backend := &preparationQueueEngine{stubEngine: &stubEngine{}}
	runtime := New(newTestStore(), WithEngine(backend))
	for _, route := range []AgentRoute{
		{ID: "first.agent", WorkflowName: "first.workflow", DefaultTaskQueue: "first.queue"},
		{ID: "second.agent", WorkflowName: "second.workflow", DefaultTaskQueue: "second.queue"},
		{ID: "third.agent", WorkflowName: "third.workflow", DefaultTaskQueue: "second.queue"},
	} {
		require.NoError(t, runtime.RegisterAgent(t.Context(), AgentRegistration{
			Definition:          NewAgentDefinition(route, nil, nil, nil, nil, nil, nil),
			Planner:             &stubPlanner{},
			WorkflowHandler:     runtime.ExecuteWorkflow,
			PlanActivityName:    string(route.ID) + ".plan",
			ResumeActivityName:  string(route.ID) + ".resume",
			ExecuteToolActivity: string(route.ID) + ".execute",
		}))
	}
	assert.Equal(t, []string{"first.queue", "second.queue", "second.queue"}, backend.queues)
	assert.Equal(t, []string{"runtime.prepare_agent_child.first.agent", "runtime.prepare_agent_child.second.agent", "runtime.prepare_agent_child.third.agent"}, backend.names)
}

func (e *preparationQueueEngine) RegisterAgentChildActivity(ctx context.Context, name string, options engine.ActivityOptions, prepare func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)) error {
	e.queues = append(e.queues, options.Queue)
	e.names = append(e.names, name)
	return e.stubEngine.RegisterAgentChildActivity(ctx, name, options, prepare)
}
