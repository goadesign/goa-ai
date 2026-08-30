package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// MCPToolset should emit registry calls and config additions.
func TestGolden_MCP_Use(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPUse())
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	exec := generatedContentBySuffix(t, files, "mcp_executor.go")
	require.Contains(t, reg, "core.NewScribeCoreMCPExecutor(caller)")
	require.Contains(t, exec, "func NewScribeCoreMCPExecutor(")
	assertGoldenGo(t, "mcp_use", "registry.go.golden", reg)
	assertGoldenGo(t, "mcp_use", "config.go.golden", cfg)
	require.Contains(t, reg, `Name: "core"`)
	require.NotContains(t, exec, "PriorInput:")
	require.NotContains(t, exec, "ExampleJSON:")
	assertGoldenGo(t, "mcp_use", "mcp_executor.go.golden", exec)
}
