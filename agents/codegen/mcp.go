package codegen

import (
	"fmt"
	"sort"

	mcpexpr "goa.design/goa-ai/features/mcp/expr"
	goaexpr "goa.design/goa/v3/expr"
)

// populateMCPToolset discovers and populates tools for external MCP toolsets by
// querying the MCP design root. It is called during toolset data construction when
// a toolset references an external MCP server (expr.External == true).
//
// The function looks up the MCP suite definition by service and suite name, then
// creates ToolData entries for each tool defined in that suite. Tool payloads and
// results are extracted from the associated MCP method definitions. Tools are sorted
// alphabetically by name for deterministic generation.
//
// If the toolset has no description, it inherits the description from the MCP suite.
// If the MCP root or suite cannot be found, the function returns early with no error,
// leaving the toolset's Tools slice empty.
func populateMCPToolset(ts *ToolsetData) {
	if ts.Expr == nil || !ts.Expr.External {
		return
	}
	if mcpexpr.Root == nil {
		return
	}
	suite := mcpexpr.Root.ServiceMCP(ts.Expr.MCPService, ts.Expr.MCPSuite)
	if suite == nil {
		return
	}
	if ts.Description == "" {
		ts.Description = suite.Description
	}
	for _, tool := range suite.Tools {
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
		td.DisplayName = fmt.Sprintf("%s.%s", ts.Name, tool.Name)
		td.QualifiedName = fmt.Sprintf("%s.%s.%s", ts.SourceServiceName, ts.Name, tool.Name)
		ts.Tools = append(ts.Tools, td)
	}
	sort.Slice(ts.Tools, func(i, j int) bool {
		return ts.Tools[i].Name < ts.Tools[j].Name
	})
}
