package dsl

import (
	"strings"

	expragents "goa.design/goa-ai/expr/agents"
	"goa.design/goa/v3/eval"
)

// FromService configures the current MCP toolset to discover tools from a Goa-backed
// service that declared an MCP server.
//
// Use FromService inside an MCP(...) block to bind the toolset to a Goa
// service's MCP suite.
//
// The suite name is derived from the MCP block name. If the name is of the form
// "<service>.<suite>", the service prefix is trimmed automatically. Otherwise, the name
// following the first dot is used as the suite name.
func FromService(service string) {
	cur, ok := eval.Current().(*expragents.ToolsetExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	service = strings.TrimSpace(service)
	if service == "" {
		eval.ReportError("mcp.FromService requires non-empty service name")
		return
	}
	cur.External = true
	cur.MCPService = service
	// Derive suite from the MCP block name.
	suite := cur.Name
	if after, ok0 := strings.CutPrefix(suite, service+"."); ok0 {
		suite = after
	} else if i := strings.Index(suite, "."); i >= 0 {
		suite = suite[i+1:]
	}
	cur.MCPSuite = suite
}

// External allows you to declare custom (non-Goa) MCP tools inline in the current
// MCP toolset. Declarations of Tool(...) inside the supplied DSL will add typed tools
// to the suite.
func External(fn func()) {
	cur, ok := eval.Current().(*expragents.ToolsetExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	cur.External = true
	if fn != nil {
		eval.Execute(fn, cur)
	}
}

// (duplicate block removed)
