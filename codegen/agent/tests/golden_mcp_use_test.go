package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// MCPToolset should emit registry calls and config additions.
func TestGolden_MCP_UseToolset(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPUseToolset())
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	require.Contains(t, reg, "NewScribeCoreMCPExecutor")
	require.Contains(t, reg, "RegisterToolset(")
	require.Contains(t, reg, "return nil")
	require.Contains(t, cfg, "type ScribeAgentConfig struct")
	require.Contains(t, cfg, "MCPCallers")
}
