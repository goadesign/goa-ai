// Package codegen checks how agent code generation reads Goa-backed MCP tools.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		MCP:  new(MCPToolsetMeta),
		Expr: &agentexpr.ToolsetExpr{
			Provider: &agentexpr.ProviderExpr{
				Kind:       agentexpr.ProviderMCP,
				MCPService: "calc",
				MCPToolset: "calc-mcp",
			},
		},
	}
	populated, err := populateMCPToolset(exact, toolset)
	require.NoError(t, err)
	require.True(t, populated)
	require.Len(t, toolset.Tools, 1)
	require.Equal(t, "exact", toolset.Tools[0].Name)
}

// TestPopulateMCPToolsetExcludesAppOnly retains ordinary and model-only tools
// while preventing app helpers from entering generated model specifications.
func TestPopulateMCPToolsetExcludesAppOnly(t *testing.T) {
	root := mcpexpr.NewRoot()
	root.MCPServers["records"] = &mcpexpr.MCPExpr{Name: "records", Tools: []*mcpexpr.ToolExpr{
		{Name: "ordinary"}, {Name: "model", Visibility: mcpexpr.ModelVisibility}, {Name: "app", Visibility: mcpexpr.AppVisibility},
	}}
	toolset := &ToolsetData{Name: "records", MCP: new(MCPToolsetMeta), Expr: &agentexpr.ToolsetExpr{Provider: &agentexpr.ProviderExpr{Kind: agentexpr.ProviderMCP, MCPService: "records", MCPToolset: "records"}}}
	populated, err := populateMCPToolset(root, toolset)
	require.NoError(t, err)
	assert.True(t, populated)
	names := make([]string, 0, len(toolset.Tools))
	for _, tool := range toolset.Tools {
		names = append(names, tool.Name)
	}
	assert.Equal(t, []string{"model", "ordinary"}, names)
}
