// Package dsl binds changing MCP catalogs to typed Goa methods. The application
// selects visible names and owns pagination; generation supplies each declared
// operation's schemas and metadata without accepting runtime tool definitions.
package dsl

import (
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// ToolCatalog selects the current unary method to list the tools visible to the
// requesting client. Its domain payload contains only an optional cursor string.
// Its result contains a tools string array and an optional nextCursor string.
// Each returned name selects a Tool declared on this service; generated code
// supplies its exact schema, description and annotations. Make tools optional
// when an empty catalog is valid. Native credentials and URL values keep their
// ordinary bindings. The method owns authorization, page order and cursor validity.
// Listing a tool never grants access to invoke it.
func ToolCatalog() {
	method, server := catalogMethod()
	if method == nil {
		return
	}
	if server.ToolCatalog != nil {
		eval.ReportError("an MCP service can declare only one tool catalog")
		return
	}
	server.ToolCatalog = method
}

// PromptCatalog selects the current unary method to list prompts visible to the
// requesting client. Its domain payload contains only an optional cursor string.
// Its result contains a prompts string array and an optional nextCursor string.
// Returned names select authored static or method-backed prompts; generation
// supplies their descriptions and argument contracts. Make prompts optional when
// an empty catalog is valid. The method owns authorization and pagination, while
// each prompt operation retains its existing authorization.
func PromptCatalog() {
	method, server := catalogMethod()
	if method == nil {
		return
	}
	if server.PromptCatalog != nil {
		eval.ReportError("an MCP service can declare only one prompt catalog")
		return
	}
	server.PromptCatalog = method
}

// catalogMethod requires a method inside an MCP service before a catalog can
// claim it. Invalid DSL placement reports an evaluation error and changes nothing.
func catalogMethod() (*expr.MethodExpr, *exprmcp.MCPExpr) {
	method, ok := eval.Current().(*expr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil, nil
	}
	server := exprmcp.Root.GetMCP(method.Service)
	if server == nil {
		eval.IncompatibleDSL()
		return nil, nil
	}
	return method, server
}
