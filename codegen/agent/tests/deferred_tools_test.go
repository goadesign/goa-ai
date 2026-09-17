// These tests run generated consumers to prove loading choices survive codegen
// and remain local to each agent sharing the same tool contracts.
package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	. "goa.design/goa-ai/dsl"
	agentexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedDeferredTools(t *testing.T) {
	for _, source := range []string{"local", "external MCP", "Goa MCP"} {
		t.Run(source, func(t *testing.T) {
			files := buildCompleteGeneratedFiles(t, func() {
				API("discovery", func() {})
				Service("provider", func() {
					if source == "Goa MCP" {
						MCP("remote", "1.0.0")
						JSONRPC(func() { POST("/rpc") })
						Method("search_method", func() {
							Payload(func() {
								Attribute("query", String, "Records to find.")
								Required("query")
							})
							Result(String)
							Tool("search", "Search records by query.")
						})
						Method("lookup_method", func() {
							Result(String)
							Tool("lookup", "Look up one record.")
						})
					}
				})
				definition := func() {
					Tool("search", "Search records by query.", func() {
						Args(func() {
							Attribute("query", String, "Records to find.")
							Required("query")
						})
						Return(String)
					})
					Tool("lookup", "Look up one record.", func() { Return(String) })
				}
				var shared *agentexpr.ToolsetExpr
				switch source {
				case "local":
					shared = Toolset("records", definition)
				case "external MCP":
					shared = Toolset("records", FromExternalMCP("provider", "remote"), definition)
				case "Goa MCP":
					shared = Toolset("records", FromMCP("provider", "remote"))
				}
				Service("discovery", func() {
					Agent("lazy", "Discover tools on demand.", func() {
						Use(shared, func() { Deferred(); Deferred() })
					})
					Agent("eager", "Expose tools immediately.", func() { Use(shared) })
					Agent("selective", "Discover search on demand.", func() {
						Use(shared, func() { Deferred("search") })
					})
					Agent("different", "Discover lookup on demand.", func() {
						Use(shared, func() { Deferred("lookup") })
					})
				})
			})
			root := writeCompleteGeneratedModule(t, files)
			literalFound := false
			for _, file := range files {
				if !strings.HasSuffix(file.Path, ".go") {
					continue
				}
				// #nosec G304 -- root and relative paths belong to this test's generated module.
				source, err := os.ReadFile(filepath.Join(root, file.Path))
				require.NoError(t, err)
				if strings.Contains(string(source), "tools.SearchDocument{") {
					literalFound = true
					assert.Contains(t, string(source), "Terms: map[string]int{")
					assert.NotContains(t, string(source), "NewSearchDocument(")
				}
			}
			require.True(t, literalFound, "generated specs must contain precomputed search data")
			configuration := ""
			runtimes := "rt, rt, rt, rt"
			registration := `executor := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, fmt.Errorf("catalog inspection must not execute a tool")
	})
	require.NoError(t, genlazy.RegisterUsedToolsets(t.Context(), rt, genlazy.WithRecordsExecutor(executor)))`
			if source != "local" {
				configuration = ", MCPCallers: map[string]mcpruntime.Caller{\"records\": caller}"
				registration = ""
				runtimes = `rt,
		runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New())),
		runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New())),
		runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))`
			}
			testSource := strings.ReplaceAll(deferredConsumerTestSource, "MCP_CONFIGURATION", configuration)
			testSource = strings.ReplaceAll(testSource, "REGISTER_LOCAL", registration)
			testSource = strings.ReplaceAll(testSource, "CONSUMER_RUNTIMES", runtimes)
			writeGeneratedPackageTest(t, root, "discovery/discovery_test.go", testSource)
			runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
		})
	}
}

const deferredConsumerTestSource = `package discovery

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	geneager "generated.local/gen/discovery/agents/eager"
	genlazy "generated.local/gen/discovery/agents/lazy"
	genselective "generated.local/gen/discovery/agents/selective"
	gendifferent "generated.local/gen/discovery/agents/different"
	"goa.design/goa-ai/runtime/agent"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

type checkingPlanner struct { deferred []string }

func (p checkingPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	definitions := input.Agent.AdvertisedToolDefinitions()
	if len(definitions) != 2 {
		return nil, fmt.Errorf("wrong generated catalog: %#v", definitions)
	}
	for _, definition := range definitions {
		if definition.Name != "records.search" && definition.Name != "records.lookup" {
			return nil, fmt.Errorf("unexpected tool %q", definition.Name)
		}
		title, description := "Search", "Search records by query."
		if definition.Name == "records.lookup" {
			title, description = "Lookup", "Look up one record."
		}
		wantSearch := tools.NewSearchDocument(definition.Name + " " + title + " " + description)
		if definition.Deferred != slices.Contains(p.deferred, definition.Name) ||
			!reflect.DeepEqual(definition.Search, wantSearch) {
			return nil, fmt.Errorf("wrong loading choice or search metadata: %#v", definition)
		}
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "catalog checked"}},
	}}}, nil
}

func (checkingPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, fmt.Errorf("unexpected resume")
}

func TestGeneratedLoadingChoice(t *testing.T) {
	rt := runtime.New(storageinmem.New(), runtime.WithEngine(engineinmem.New()))
	runtimes := []*runtime.Runtime{CONSUMER_RUNTIMES}
	caller := mcpruntime.CallerFunc(func(context.Context, mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
		return mcpruntime.CallResponse{}, fmt.Errorf("catalog inspection must not call MCP")
	})
	require.NotNil(t, caller)
	require.NoError(t, genlazy.RegisterLazyAgent(t.Context(), runtimes[0], genlazy.LazyAgentConfig{
		Planner: checkingPlanner{deferred: []string{"records.search", "records.lookup"}}MCP_CONFIGURATION,
	}))
	require.NoError(t, geneager.RegisterEagerAgent(t.Context(), runtimes[1], geneager.EagerAgentConfig{
		Planner: checkingPlanner{}MCP_CONFIGURATION,
	}))
	require.NoError(t, genselective.RegisterSelectiveAgent(t.Context(), runtimes[2], genselective.SelectiveAgentConfig{
		Planner: checkingPlanner{deferred: []string{"records.search"}}MCP_CONFIGURATION,
	}))
	require.NoError(t, gendifferent.RegisterDifferentAgent(t.Context(), runtimes[3], gendifferent.DifferentAgentConfig{
		Planner: checkingPlanner{deferred: []string{"records.lookup"}}MCP_CONFIGURATION,
	}))
	REGISTER_LOCAL
	messages := []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "check catalog"}},
	}}
	for i, id := range []agent.Ident{genlazy.AgentID, geneager.AgentID, genselective.AgentID, gendifferent.AgentID} {
		_, err := runtimes[i].MustClient(id).OneShotRun(t.Context(), messages)
		require.NoError(t, err)
	}
}
`
