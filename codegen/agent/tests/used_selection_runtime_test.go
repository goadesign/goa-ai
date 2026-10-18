// These tests construct and run generated agents with different tool selections.
// Root definitions and embedded children must use matching catalogs, executable
// names and deferred names, while shared provider registration stays complete.
// JSON catalogs must match the agent specifications, including exported tools.
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
			Agent("all", "Use every record operation.", func() { Use(records) })
			Agent("deferred", "Find every record operation on demand.", func() {
				Use(records, func() { Deferred() })
			})
			Agent("remote_reader", "Consume one remote operation.", func() {
				Use(owned, func() { Tool("first") })
			})
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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	genallspecs "generated.local/gen/discovery/agents/all/specs"
	gendeferredspecs "generated.local/gen/discovery/agents/deferred/specs"
	genparent "generated.local/gen/discovery/agents/parent"
	genparentspecs "generated.local/gen/discovery/agents/parent/specs"
	genremotereaderspecs "generated.local/gen/discovery/agents/remote_reader/specs"
	genowner "generated.local/gen/discovery/agents/owner"
	genownerspecs "generated.local/gen/discovery/agents/owner/specs"
	genowned "generated.local/gen/remote/toolsets/owned"
	genrecords "generated.local/gen/discovery/toolsets/records"
	genspecialist "generated.local/gen/discovery/agents/specialist"
	genspecialistspecs "generated.local/gen/discovery/agents/specialist/specs"
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

// Each agent's JSON file contains its selected tools and any complete exports,
// with the same identifiers and result schemas as its generated Go catalog.
func TestSelectedAgentJSONCatalogMatchesSpecs(t *testing.T) {
	wantRoutes := map[string]string{
		"records.lookup": "discovery.records",
		"records.search": "discovery.records",
		"actions.ask": "actions",
		"actions.summarize": "actions",
		"owned.first": "owned",
		"owned.second": "owned",
	}
	for _, test := range []struct {
		name string
		service string
		specs []tools.ToolSpec
		want []string
	}{
		{"all", "discovery", genallspecs.Specs(), []string{"records.lookup", "records.search"}},
		{"deferred", "discovery", gendeferredspecs.Specs(), []string{"records.lookup", "records.search"}},
		{"remote_reader", "remote", genremotereaderspecs.Specs(), []string{"owned.first"}},
		{"owner", "remote", genownerspecs.Specs(), []string{"owned.first", "owned.second"}},
		{"parent", "discovery", genparentspecs.Specs(), []string{"actions.ask", "records.lookup"}},
		{"specialist", "discovery", genspecialistspecs.Specs(), []string{"actions.ask", "actions.summarize", "records.search"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(filepath.Join("..", "gen", "discovery", "agents", test.name, "specs", "tool_schemas.json"))
			require.NoError(t, err)
			var catalog struct {
				Tools []struct {
					ID string
					Service string
					Toolset string
					Description string
					Result struct {
						Name string
						Schema json.RawMessage
					}
				}
			}
			require.NoError(t, json.Unmarshal(payload, &catalog))
			byName := make(map[string]tools.ToolSpec, len(test.specs))
			specIDs := make([]string, 0, len(test.specs))
			for _, spec := range test.specs {
				id := string(spec.Name)
				specIDs = append(specIDs, id)
				byName[id] = spec
			}
			slices.Sort(specIDs)
			require.Equal(t, test.want, specIDs)
			catalogIDs := make([]string, 0, len(catalog.Tools))
			for _, tool := range catalog.Tools {
				catalogIDs = append(catalogIDs, tool.ID)
				spec, ok := byName[tool.ID]
				require.True(t, ok, "JSON includes an unselected tool %q", tool.ID)
				require.Equal(t, test.service, tool.Service)
				wantRoute, ok := wantRoutes[tool.ID]
				require.True(t, ok, "missing expected route for %q", tool.ID)
				require.Equal(t, wantRoute, tool.Toolset)
				require.Equal(t, spec.Description, tool.Description)
				require.Equal(t, spec.Result.Name, tool.Result.Name)
				require.JSONEq(t, string(spec.Result.Schema), string(tool.Result.Schema))
			}
			require.Equal(t, specIDs, catalogIDs)
		})
	}
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
