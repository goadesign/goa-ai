package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/testutil"
)

// MCP DSL should emit the same registry/config scaffolding as MCPToolset.
func TestGolden_MCP_DSL(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MCPDSL())
	reg := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	cfg := fileContent(t, files, "gen/alpha/agents/scribe/config.go")
	testutil.AssertGo(t, "testdata/golden/mcp_dsl/registry.go.golden", reg)
	testutil.AssertGo(t, "testdata/golden/mcp_dsl/config.go.golden", cfg)

	// Keep a lightweight structural marker for agent.go to avoid brittleness.
	agent := fileContent(t, files, "gen/alpha/agents/scribe/agent.go")
	require.Contains(t, agent, "const AgentID agent.Ident")
}
