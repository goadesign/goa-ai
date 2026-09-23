// These tests run the child definition embedded in a generated consumer to
// check that each agent retains its own exact tool-loading choice.
package tests

import (
	"os/exec"
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedDeferredAgentTools(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, func() {
		API("discovery", func() {})
		records := Toolset("records", func() {
			Tool("search", "Search records.", func() { Return(String) })
			Tool("lookup", "Look up a record.", func() { Return(String) })
		})
		actions := Toolset("actions", func() {
			Tool("ask", "Ask the specialist.", func() { Return(String) })
			Tool("summarize", "Summarize with the specialist.", func() { Return(String) })
		})
		Service("discovery", func() {
			Agent("specialist", "Read records.", func() {
				Use(records, func() { Deferred("search") })
				Export(actions)
			})
			Agent("parent", "Delegate and read records.", func() {
				Use(records, func() { Deferred("lookup") })
				Use(AgentToolset("discovery", "specialist", "actions"), func() { Deferred("ask") })
			})
		})
	})
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "discovery/agent_tools_test.go", deferredAgentToolsTestSource)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
}

const deferredAgentToolsTestSource = `package discovery

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	genparent "generated.local/gen/discovery/agents/parent"
	genspecialist "generated.local/gen/discovery/agents/specialist"
	genactions "generated.local/gen/discovery/agents/specialist/agenttools/actions"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type catalogPlanner struct{ expected map[string]bool }

func (p catalogPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	definitions := input.Agent.AdvertisedToolDefinitions()
	if len(definitions) != len(p.expected) {
		return nil, fmt.Errorf("unexpected catalog: %#v", definitions)
	}
	for _, definition := range definitions {
		deferred, ok := p.expected[definition.Name]
		if !ok || definition.Deferred != deferred || len(definition.Search.Terms) == 0 {
			return nil, fmt.Errorf("wrong consumer loading choice: %#v", definition)
		}
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "catalog checked"}},
	}}}, nil
}

func (catalogPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, fmt.Errorf("unexpected resume")
}

func TestEmbeddedChildKeepsOwnDeferral(t *testing.T) {
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.NoError(t, genparent.RegisterParentAgent(t.Context(), rt, genparent.ParentAgentConfig{
		Planner: catalogPlanner{expected: map[string]bool{
			"records.search": false, "records.lookup": true, "actions.ask": true, "actions.summarize": false,
		}},
	}))
	child, ok := genparent.Definition().ChildDefinition(genspecialist.AgentID)
	require.True(t, ok)
	require.NoError(t, rt.RegisterAgent(t.Context(), runtime.AgentRegistration{
		Definition: child,
		Planner: catalogPlanner{expected: map[string]bool{"records.search": true, "records.lookup": false}},
		WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: genspecialist.PlanActivity,
		ResumeActivityName: genspecialist.ResumeActivity,
		ExecuteToolActivity: genspecialist.ExecuteToolActivity,
	}))
	executor := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, fmt.Errorf("catalog inspection must not execute a tool")
	})
	require.NoError(t, genparent.RegisterUsedToolsets(t.Context(), rt, genparent.WithRecordsExecutor(executor)))
	actions, err := genactions.NewRegistration(child, "")
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(actions))
	for _, id := range []agent.Ident{genparent.AgentID, genspecialist.AgentID} {
		_, err := rt.MustClient(id).OneShotRun(t.Context(), []*model.Message{{
			Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "check catalog"}},
		}})
		require.NoError(t, err)
	}
}
`
