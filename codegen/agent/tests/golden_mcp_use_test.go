package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// MCPToolset should emit registry calls and config additions.
func TestGolden_MCP_Use(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPUse())
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	assertGoldenGo(t, "mcp_use", "registry.go.golden", reg)
	assertGoldenGo(t, "mcp_use", "config.go.golden", cfg)
}
