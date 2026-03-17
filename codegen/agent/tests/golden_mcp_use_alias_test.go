package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Aliased MCP toolsets should keep provider-owned specs paths while preserving
// the local definition name in generated registry/config output.
func TestGolden_MCP_UseAlias(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPUseAlias())
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	assertGoldenGo(t, "mcp_use_alias", "registry.go.golden", reg)
	assertGoldenGo(t, "mcp_use_alias", "config.go.golden", cfg)
}
