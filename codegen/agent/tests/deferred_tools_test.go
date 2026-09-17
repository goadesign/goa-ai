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
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedDeferredTools(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, func() {
		API("discovery", func() {})
		shared := Toolset("records", func() {
			Tool("search", "Search records by query.", func() {
				Args(func() {
					Attribute("query", String, "Records to find.")
					Required("query")
				})
				Return(String)
			})
		})
		Service("discovery", func() {
			Agent("lazy", "Discover tools on demand.", func() {
				Use(shared, func() { Deferred() })
			})
			Agent("eager", "Expose tools immediately.", func() {
				Use(shared)
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
	writeGeneratedPackageTest(t, root, "discovery/discovery_test.go", deferredConsumerTestSource)
	runGeneratedGoTestCommand(t, root, exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./..."))
}

const deferredConsumerTestSource = `package discovery

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	geneager "generated.local/gen/discovery/agents/eager"
	genlazy "generated.local/gen/discovery/agents/lazy"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type checkingPlanner struct { deferred bool }

func (p checkingPlanner) PlanStart(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	definitions := input.Agent.AdvertisedToolDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "records.search" ||
		definitions[0].Deferred != p.deferred || definitions[0].Search.Terms["records"] == 0 {
		return nil, fmt.Errorf("wrong generated catalog: %#v", definitions)
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
	require.NoError(t, genlazy.RegisterLazyAgent(t.Context(), rt, genlazy.LazyAgentConfig{
		Planner: checkingPlanner{deferred: true},
	}))
	require.NoError(t, geneager.RegisterEagerAgent(t.Context(), rt, geneager.EagerAgentConfig{
		Planner: checkingPlanner{},
	}))
	executor := runtime.ToolCallExecutorFunc(func(context.Context, *runtime.ToolCallMeta, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
		return nil, fmt.Errorf("catalog inspection must not execute a tool")
	})
	require.NoError(t, genlazy.RegisterUsedToolsets(t.Context(), rt, genlazy.WithRecordsExecutor(executor)))
	messages := []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "check catalog"}},
	}}
	_, err := rt.MustClient(genlazy.AgentID).OneShotRun(t.Context(), messages)
	require.NoError(t, err)
	_, err = rt.MustClient(geneager.AgentID).OneShotRun(t.Context(), messages)
	require.NoError(t, err)
}
`
