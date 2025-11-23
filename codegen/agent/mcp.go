package codegen

import (
	"fmt"
	"sort"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	goaexpr "goa.design/goa/v3/expr"
)

// populateMCPToolset discovers and populates tools for external MCP toolsets by
// querying the MCP design root. It is called during toolset data construction when
// a toolset references an external MCP server (expr.External == true).
//
// The function looks up the MCP server/toolset definition by service and toolset
// name, then creates ToolData entries for each tool defined in that toolset. Tool
// payloads and
// results are extracted from the associated MCP method definitions. Tools are sorted
// alphabetically by name for deterministic generation.
//
// If the toolset has no description, it inherits the description from the MCP
// server/toolset. If the MCP root or toolset cannot be found, the function
// returns early with no error, leaving the toolset's Tools slice empty.
// populateMCPToolset returns true when an MCP server/toolset defined in the Goa
// design was found and used to populate tools. When false is returned, callers
// may choose to populate tools from inline tool declarations (custom external
// MCP).
func populateMCPToolset(ts *ToolsetData) bool {
	if ts.Expr == nil || !ts.Expr.External {
		return false
	}
	if mcpexpr.Root == nil {
		return false
	}
	mcp := mcpexpr.Root.ServiceMCP(ts.Expr.MCPService, ts.Expr.MCPToolset)
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
		}
		td.Title = humanizeTitle(tool.Name)
		td.QualifiedName = fmt.Sprintf("%s.%s", ts.Name, tool.Name)
		ts.Tools = append(ts.Tools, td)
	}
	sort.Slice(ts.Tools, func(i, j int) bool {
		return ts.Tools[i].Name < ts.Tools[j].Name
	})
	return true
}
