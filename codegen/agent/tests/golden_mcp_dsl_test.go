package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// MCP DSL should emit the same registry/config scaffolding as MCPToolset.
func TestGolden_MCP_DSL(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPDSL())
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	agent := fileContent(t, files, "gen/alpha/agents/scribe/agent.go")
	require.Contains(t, reg, "NewScribeCoreMCPExecutor")
	require.Contains(t, reg, "RegisterToolset(")
	require.Contains(t, reg, "return nil")
	require.Contains(t, cfg, "type ScribeAgentConfig struct")
	require.Contains(t, cfg, "MCPCallers")
	require.Contains(t, cfg, "WithMCPCaller(")
	require.Contains(t, agent, "const AgentID agent.Ident")
}
