package dsl

import (
	"strings"

	expragents "goa.design/goa-ai/expr/agents"
	"goa.design/goa/v3/eval"
)

// MCP declares an MCP toolset under the current agent Uses block with a composable
// options DSL. Inside the MCP block, use the functions from the dsl/mcp
// subpackage to configure the suite:
//   - mcp.FromService("calc") to discover tools from a Goa-backed MCP service
//   - mcp.External(func() { Tool(...); ... }) to declare custom tools inline
//   - Description("...") and Tags("...") for metadata
//
// Example:
//
//	Agent("docs-agent", "Agent description", func() {
//	    Uses(func() {
//	        MCP("calc.core", func() {
//	           // mcp.FromService("calc")
//	        })
//	        MCP("bloop", func() {
//	            // mcp.External(func() { Tool("search", "...", func() { /* ... */ }) })
//	        })
//	    })
//	})
func MCP(suite string, dsl func()) {
	group, ok := eval.Current().(*expragents.ToolsetGroupExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	suite = strings.TrimSpace(suite)
	if suite == "" {
		eval.ReportError("MCP requires non-empty suite name")
		return
	}
	// Default to the current agent service for service-qualified name
	serviceName := ""
	if group.Agent != nil && group.Agent.Service != nil {
		serviceName = group.Agent.Service.Name
	}
	ts := &expragents.ToolsetExpr{
		Name:       suite,
		Agent:      group.Agent,
		External:   true,
		MCPService: serviceName,
		MCPSuite:   suite,
		DSLFunc:    dsl,
	}
	group.Toolsets = append(group.Toolsets, ts)
}
