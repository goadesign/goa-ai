package tests

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// buildAndGenerateExample provided by golden_helpers_test.go

func TestExampleInternal_MethodBacked(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MethodComplexEmbedded())

	// Bootstrap
	boot := fileContent(t, files, "internal/agents/alpha/bootstrap/bootstrap.go")
	assertGoldenGo(t, "example_internal_method", "bootstrap.go.golden", boot)

	// Planner stub
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	require.NotContains(t, plan, "decode generated")
	require.Contains(t, plan, "Hello from example planner.")
	assertGoldenGo(t, "example_internal_method", "planner.go.golden", plan)

	// Executor stub for toolset profiles
	exec := fileContent(t, files, "internal/agents/scribe/toolsets/profiles/execute.go")
	require.NotContains(t, exec, `rawjson.Message("")`)
	require.NotContains(t, exec, `"goa.design/goa-ai/runtime/agent/rawjson"`)
	require.Contains(t, exec, "generated executor requires an application implementation")
	assertGoldenGo(t, "example_internal_method", "executor.go.golden", exec)
}

func TestExampleInternal_MCP(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MCPUse())

	// Bootstrap should include MCP caller stubs
	boot := fileContent(t, files, "internal/agents/alpha/bootstrap/bootstrap.go")
	require.Contains(t, boot, `ClientInfo: mcpruntime.ClientInfo{Name: "alpha", Version: "1.2.3"}`)
	require.NotContains(t, boot, "ProtocolVersion:")
	assertGoldenGo(t, "example_internal_mcp", "bootstrap.go.golden", boot)

	// Planner stub exists
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	assertGoldenGo(t, "example_internal_mcp", "planner.go.golden", plan)
}

// TestExampleInternalSeparatesServiceBootstraps checks that each service
// command starts only the agents declared by that service.
func TestExampleInternalSeparatesServiceBootstraps(t *testing.T) {
	files := buildAndGenerateExample(t, multiServiceExampleDesign())

	alphaBootstrap := renderedFileContent(t, files, "internal/agents/alpha/bootstrap/bootstrap.go")
	require.Contains(t, alphaBootstrap, `"goa.design/goa-ai/gen/alpha/agents/alpha_worker"`)
	require.NotContains(t, alphaBootstrap, "beta_worker")

	betaBootstrap := renderedFileContent(t, files, "internal/agents/beta/bootstrap/bootstrap.go")
	require.Contains(t, betaBootstrap, `"goa.design/goa-ai/gen/beta/agents/beta_worker"`)
	require.NotContains(t, betaBootstrap, "alpha_worker")

	alphaMain := renderedFileContent(t, files, "cmd/alpha/main.go")
	require.Contains(t, alphaMain, `"goa.design/goa-ai/internal/agents/alpha/bootstrap"`)
	require.NotContains(t, alphaMain, "internal/agents/beta/bootstrap")

	betaMain := renderedFileContent(t, files, "cmd/beta/main.go")
	require.Contains(t, betaMain, `"goa.design/goa-ai/internal/agents/beta/bootstrap"`)
	require.NotContains(t, betaMain, "internal/agents/alpha/bootstrap")
}

// TestExampleInternalUsesGeneratedToolNames checks that independently planned
// example files call the exact names written by goa gen.
func TestExampleInternalUsesGeneratedToolNames(t *testing.T) {
	design := exampleToolNameCollisionDesign()
	generated := buildAndGenerate(t, design)
	specs := fileContent(t, generated, "gen/alpha/toolsets/ops/specs.go")

	examples := buildAndGenerateExample(t, design)
	executor := renderedFileContent(t, examples, "internal/agents/worker/toolsets/ops/execute.go")

	toolRefs := regexp.MustCompile(`opsspecs\.([A-Za-z0-9]+)\(\)\.Payload\.FromJSON`).FindAllStringSubmatch(executor, -1)
	require.Len(t, toolRefs, 2)
	for _, match := range toolRefs {
		require.Contains(t, specs, "func "+match[1]+"() tools.TypedTool")
	}
	for _, file := range examples {
		require.False(t, strings.HasPrefix(file.Path, "gen/"), file.Path)
	}
}

// exampleToolNameCollisionDesign defines two method-backed tools whose names
// become the same Go name before the package chooses unique spellings.
func exampleToolNameCollisionDesign() func() {
	return func() {
		API("example_names", func() {})
		Service("alpha", func() {
			Method("by_dash", func() {
				Payload(func() {
					Attribute("id", String, "Item ID")
					Required("id")
				})
				Result(String)
			})
			Method("by_underscore", func() {
				Payload(func() {
					Attribute("id", String, "Item ID")
					Required("id")
				})
				Result(String)
			})
			Agent("worker", "Worker", func() {
				Use("ops", func() {
					Tool("by-id", "Find by ID", func() {
						BindTo("by_dash")
					})
					Tool("by_id", "Find by ID", func() {
						BindTo("by_underscore")
					})
				})
			})
		})
	}
}

// multiServiceExampleDesign defines two services that each own one agent.
func multiServiceExampleDesign() func() {
	return func() {
		API("multi_service", func() {})
		Service("alpha", func() {
			Agent("alpha_worker", "Handles alpha work", func() {})
		})
		Service("beta", func() {
			Agent("beta_worker", "Handles beta work", func() {})
		})
	}
}
