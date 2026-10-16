// These tests construct and run generated agents with different tool selections.
// Root definitions and embedded children must use matching catalogs, executable
// names and deferred names, while shared provider registration stays complete.
package tests

import (
	"os/exec"
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedSelectedAgentDefinitions(t *testing.T) {
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
		owned := Toolset("owned", FromExternalMCP("remote", "records"), func() {
			Tool("first", "Use the first operation.", func() { Return(String) })
			Tool("second", "Use the second operation.", func() { Return(String) })
		})
		Service("remote", func() {})
		Service("discovery", func() {
			Agent("owner", "Consume one operation and export both.", func() {
				Use(owned, func() {
					Tool("first")
					Deferred()
				})
				Export(owned)
			})
			Agent("specialist", "Read records.", func() {
				Use(records, func() { Tool("search"); Deferred() })
				Export(actions)
			})
			Agent("parent", "Delegate and read records.", func() {
				Use(records, func() { Tool("lookup"); Deferred() })
				Use(AgentToolset("discovery", "specialist", "actions"), func() { Tool("ask"); Deferred() })
			})
		})
	})
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "discovery/agent_tools_test.go", selectedAgentDefinitionsTestSource)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
}

const selectedAgentDefinitionsTestSource = `package discovery

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	genparent "generated.local/gen/discovery/agents/parent"
	genowner "generated.local/gen/discovery/agents/owner"
	genownerspecs "generated.local/gen/discovery/agents/owner/specs"
	genowned "generated.local/gen/remote/toolsets/owned"
	genrecords "generated.local/gen/discovery/toolsets/records"
	genspecialist "generated.local/gen/discovery/agents/specialist"
	genactions "generated.local/gen/discovery/agents/specialist/agenttools/actions"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type catalogPlanner struct {
	expected map[string]bool
	call tools.Ident
}

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
	if p.call != "" {
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name: p.call, Payload: rawjson.Message("{}"),
		}}}, nil
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "catalog checked"}},
	}}}, nil
}

func (p catalogPlanner) PlanResume(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if p.call == "" || len(input.ToolOutputs) != 1 {
		return nil, fmt.Errorf("unexpected resume outputs: %#v", input.ToolOutputs)
	}
	output := input.ToolOutputs[0]
	if output.Name != p.call || output.Failure != nil || string(output.Result) != fmt.Sprintf("%q", string(p.call)+" completed") {
		return nil, fmt.Errorf("unexpected completed tool result: %#v", output)
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: string(p.call)+" verified"}},
	}}}, nil
}

func TestSelectedRootAndEmbeddedChildConstructAndRun(t *testing.T) {
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.Len(t, genrecords.Specs(), 2)
	require.Len(t, genowned.Specs(), 2)
	require.Len(t, genownerspecs.Specs(), 2)
	require.NoError(t, genowner.RegisterOwnerAgent(t.Context(), rt, genowner.OwnerAgentConfig{
		Planner: catalogPlanner{expected: map[string]bool{"owned.first": true}},
	}))
	require.NoError(t, genparent.RegisterParentAgent(t.Context(), rt, genparent.ParentAgentConfig{
		Planner: catalogPlanner{expected: map[string]bool{
			"records.lookup": true, "actions.ask": true,
		}},
	}))
	child, ok := genparent.Definition().ChildDefinition(genspecialist.AgentID)
	require.True(t, ok)
	require.NoError(t, rt.RegisterAgent(t.Context(), runtime.AgentRegistration{
		Definition: child,
		Planner: catalogPlanner{expected: map[string]bool{"records.search": true}},
		WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: genspecialist.PlanActivity,
		ResumeActivityName: genspecialist.ResumeActivity,
		ExecuteToolActivity: genspecialist.ExecuteToolActivity,
	}))
	executor := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, fmt.Errorf("catalog inspection must not execute a tool")
	})
	require.NoError(t, genparent.RegisterUsedToolsets(t.Context(), rt, genparent.WithRecordsExecutor(executor)))
	require.NoError(t, genowner.RegisterUsedToolsets(t.Context(), rt, genowner.WithOwnedExecutor(executor)))
	actions, err := genactions.NewRegistration(child, "")
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(actions))
	for _, id := range []agent.Ident{genparent.AgentID, genspecialist.AgentID, genowner.AgentID} {
		_, err := rt.MustClient(id).OneShotRun(t.Context(), []*model.Message{{
			Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "check catalog"}},
		}})
		require.NoError(t, err)
	}
}

// Both agents share one complete registered provider. Each planner invokes its
// own selected operation and validates the completed result before finishing.
func TestSelectedConsumersDispatchDifferentSharedTools(t *testing.T) {
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	require.Len(t, genrecords.Specs(), 2)
	require.NoError(t, genparent.RegisterParentAgent(t.Context(), rt, genparent.ParentAgentConfig{
		Planner: catalogPlanner{
			expected: map[string]bool{"records.lookup": true, "actions.ask": true},
			call: "records.lookup",
		},
	}))
	child, ok := genparent.Definition().ChildDefinition(genspecialist.AgentID)
	require.True(t, ok)
	require.NoError(t, rt.RegisterAgent(t.Context(), runtime.AgentRegistration{
		Definition: child,
		Planner: catalogPlanner{
			expected: map[string]bool{"records.search": true},
			call: "records.search",
		},
		WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: genspecialist.PlanActivity,
		ResumeActivityName: genspecialist.ResumeActivity,
		ExecuteToolActivity: genspecialist.ExecuteToolActivity,
	}))
	calls := make(chan tools.Ident, 2)
	executor := runtime.ToolCallExecutorFunc(func(_ context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		select {
		case calls <- call.Name:
		default:
			return nil, fmt.Errorf("unexpected extra shared-provider invocation")
		}
		return runtime.Executed(&planner.ToolResult{
			Name: call.Name,
			ToolCallID: call.ToolCallID,
			Result: string(call.Name)+" completed",
		}), nil
	})
	require.NoError(t, genparent.RegisterUsedToolsets(t.Context(), rt, genparent.WithRecordsExecutor(executor)))
	actions, err := genactions.NewRegistration(child, "")
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(actions))
	for _, test := range []struct {
		agent agent.Ident
		tool tools.Ident
	}{
		{genparent.AgentID, "records.lookup"},
		{genspecialist.AgentID, "records.search"},
	} {
		result, err := rt.MustClient(test.agent).OneShotRun(t.Context(), []*model.Message{{
			Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "execute selected operation"}},
		}})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, string(test.tool)+" verified", result.Final.Text())
		select {
		case called := <-calls:
			require.Equal(t, test.tool, called)
		default:
			t.Fatal("run completed without invoking the shared executor")
		}
	}
	require.Empty(t, calls)
}
`
