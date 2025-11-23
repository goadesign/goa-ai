package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// buildAndGenerateExample provided by golden_helpers_test.go

func TestExampleInternal_MethodBacked(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MethodComplexEmbedded())

	// Bootstrap
	boot := fileContent(t, files, "internal/agents/bootstrap/bootstrap.go")
	assertGoldenGo(t, "example_internal_method", "bootstrap.go.golden", boot)

	// Planner stub
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	assertGoldenGo(t, "example_internal_method", "planner.go.golden", plan)

	// Executor stub for toolset profiles
	exec := fileContent(t, files, "internal/agents/scribe/toolsets/profiles/execute.go")
	assertGoldenGo(t, "example_internal_method", "executor.go.golden", exec)
}

func TestExampleInternal_MCP(t *testing.T) {
	files := buildAndGenerateExample(t, testscenarios.MCPUse())

	// Bootstrap should include MCP caller stubs
	boot := fileContent(t, files, "internal/agents/bootstrap/bootstrap.go")
	assertGoldenGo(t, "example_internal_mcp", "bootstrap.go.golden", boot)

	// Planner stub exists
	plan := fileContent(t, files, "internal/agents/scribe/planner/planner.go")
	assertGoldenGo(t, "example_internal_mcp", "planner.go.golden", plan)
}
