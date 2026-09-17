// Agent definitions own loading choices independently of the immutable shared
// schemas used to validate and execute tools.
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDeferredToolsAreScopedToAgentDefinition(t *testing.T) {
	rt := New(newTestStore(), WithEngine(&stubEngine{}))
	spec := newAnyJSONSpec("records.search")
	spec.Search = tools.NewSearchDocument("records.search Search records")
	metadata := func(name tools.Ident) (policy.ToolMetadata, bool) {
		return policy.ToolMetadata{
			ID: name, Title: "Search records", BudgetClass: policy.ToolBudgetClassBudgeted,
		}, name == spec.Name
	}
	for _, id := range []agent.Ident{"records.deferred", "records.immediate"} {
		var deferred []tools.Ident
		if id == "records.deferred" {
			deferred = []tools.Ident{spec.Name}
		}
		definition := NewAgentDefinition(
			AgentRoute{ID: id, WorkflowName: string(id) + ".workflow", DefaultTaskQueue: string(id)},
			[]tools.ToolSpec{spec}, metadata, nil, []tools.Ident{spec.Name}, nil, deferred,
		)
		require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
			Definition:          definition,
			Planner:             &stubPlanner{},
			WorkflowHandler:     (engine.WorkflowDefinition{Handler: rt.ExecuteWorkflow}).Handler,
			PlanActivityName:    string(id) + ".plan",
			ResumeActivityName:  string(id) + ".resume",
			ExecuteToolActivity: string(id) + ".execute",
		}))
		if len(deferred) > 0 {
			deferred[0] = "changed"
		}
	}
	deferred := newAgentContext(agentContextOptions{runtime: rt, agentID: "records.deferred"})
	immediate := newAgentContext(agentContextOptions{runtime: rt, agentID: "records.immediate"})
	got := deferred.AdvertisedToolDefinitions()
	require.Len(t, got, 1)
	assert.True(t, got[0].Deferred)
	assert.Equal(t, spec.Search, got[0].Search)
	assert.False(t, immediate.AdvertisedToolDefinitions()[0].Deferred)
	got[0].Deferred = false
	got[0].Search.Terms["records"] = 100
	assert.Equal(t, 2, deferred.AdvertisedToolDefinitions()[0].Search.Terms["records"])
	assert.True(t, deferred.AdvertisedToolDefinitions()[0].Deferred)
	assert.False(t, rt.toolDefinitions[spec.Name].Deferred)
}

func TestDeferredDefinitionRejectsUnknownAndRepeatedTools(t *testing.T) {
	spec := newAnyJSONSpec("records.search")
	for _, names := range [][]tools.Ident{{"unknown"}, {spec.Name, spec.Name}} {
		assert.Panics(t, func() {
			NewAgentDefinition(
				AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"},
				[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, names,
			)
		})
	}
}
