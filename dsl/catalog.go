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
	method, server := mcpMethod()
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
	method, server := mcpMethod()
	if method == nil {
		return
	}
	if server.PromptCatalog != nil {
		eval.ReportError("an MCP service can declare only one prompt catalog")
		return
	}
	server.PromptCatalog = method
}

// ResourceCatalog selects the current unary method to list resource descriptors.
// Its domain payload contains only an optional cursor string. Its result contains
// resources, an optional ArrayOfRequired of objects, and optional nextCursor.
// Each object declares required uri and name strings; uri uses FormatURI.
// Optional title, description, mimeType, icons, annotations, size and _meta retain
// their current MCP shapes and validation. ResourceReader owns exact URI reads.
// The catalog method owns authorization, page order and cursor validity.
func ResourceCatalog() {
	method, server := mcpMethod()
	if method == nil {
		return
	}
	if server.ResourceCatalog != nil {
		eval.ReportError("an MCP service can declare only one resource catalog")
		return
	}
	server.ResourceCatalog = method
}

// ResourceTemplateCatalog selects the current unary method to list URI templates.
// Its domain payload contains only optional cursor. Its result contains optional
// resourceTemplates, an ArrayOfRequired of objects, and optional nextCursor.
// Each object declares required uriTemplate and name strings. Optional title,
// description, mimeType, icons, annotations and _meta retain current MCP shapes.
// The service's ResourceReader receives expanded URIs without inferred variables.
func ResourceTemplateCatalog() {
	method, server := mcpMethod()
	if method == nil {
		return
	}
	if server.ResourceTemplateCatalog != nil {
		eval.ReportError("an MCP service can declare only one resource template catalog")
		return
	}
	server.ResourceTemplateCatalog = method
}

// mcpMethod requires an authored method inside an MCP service before a binding
// can claim it. Invalid placement reports an evaluation error and changes nothing.
func mcpMethod() (*expr.MethodExpr, *exprmcp.MCPExpr) {
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
