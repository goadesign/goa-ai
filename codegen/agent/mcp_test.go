// Package codegen checks how agent code generation reads Goa-backed MCP tools.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
)

func TestPopulateMCPToolsetUsesPassedRoot(t *testing.T) {
	exact := mcpexpr.NewRoot()
	exact.MCPServers["calc"] = &mcpexpr.MCPExpr{
		Name:  "calc-mcp",
		Tools: []*mcpexpr.ToolExpr{{Name: "exact"}},
	}
	other := mcpexpr.NewRoot()
	other.MCPServers["calc"] = &mcpexpr.MCPExpr{
		Name:  "calc-mcp",
		Tools: []*mcpexpr.ToolExpr{{Name: "other"}},
	}
	previous := mcpexpr.Root
	mcpexpr.Root = other
	t.Cleanup(func() { mcpexpr.Root = previous })

	toolset := &ToolsetData{
		Name: "remote",
		Expr: &agentexpr.ToolsetExpr{
			Provider: &agentexpr.ProviderExpr{
				Kind:       agentexpr.ProviderMCP,
				MCPService: "calc",
				MCPToolset: "calc-mcp",
			},
		},
	}
	require.True(t, populateMCPToolset(exact, toolset))
	require.Len(t, toolset.Tools, 1)
	require.Equal(t, "exact", toolset.Tools[0].Name)
}
