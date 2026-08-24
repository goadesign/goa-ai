// This file reads Goa-backed MCP tool definitions for agent code generation.
package codegen

import (
	"fmt"
	"sort"

	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	goaexpr "goa.design/goa/v3/expr"
)

// populateMCPToolset reads the named server from the MCP definitions supplied
// to this generation and adds its tools to ts. It returns false when the toolset
// or server is missing.
func populateMCPToolset(mcpRoot *mcpexpr.RootExpr, ts *ToolsetData) bool {
	if ts.Expr == nil || ts.Expr.Provider == nil || ts.Expr.Provider.Kind != agentsExpr.ProviderMCP {
		return false
	}
	if mcpRoot == nil {
		return false
	}
	mcp := mcpRoot.ServiceMCP(ts.Expr.Provider.MCPService, ts.Expr.Provider.MCPToolset)
	if mcp == nil {
		return false
	}
	if ts.Description == "" {
		ts.Description = mcp.Description
	}
	for _, tool := range mcp.Tools {
		var payload, result *goaexpr.AttributeExpr
		if tool.Method != nil {
			payload = tool.Method.Payload
			result = tool.Method.Result
		}
		td := &ToolData{
			Name:        tool.Name,
			Description: tool.Description,
			Args:        payload,
			Return:      result,
			Toolset:     ts,
			HasResult:   result != nil && result.Type != goaexpr.Empty,
		}
		td.Title = naming.HumanizeTitle(tool.Name)
		td.QualifiedName = fmt.Sprintf("%s.%s", ts.Name, tool.Name)
		ts.Tools = append(ts.Tools, td)
	}
	sort.Slice(ts.Tools, func(i, j int) bool {
		return ts.Tools[i].Name < ts.Tools[j].Name
	})
	return true
}
