// Package dsl declares parameterized resource catalogs. The service receives the
// client's URI unchanged and owns finding and authorizing its contents.
package dsl

import (
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// ResourceReader selects the current unary method to read exact resource URIs.
// Its payload contains only a required uri string apart from native credentials,
// mapped URL values and an InputExchange continuation. Its completed result
// contains only contents, an ArrayOfRequired of objects with one required content
// OneOf using text/blob branches. Each branch carries a URI and either text or
// binary bytes. The method owns existence, access checks and URI interpretation.
// Discovery may return no entries; reading never depends on a prior list request.
func ResourceReader() {
	method, server := mcpMethod()
	if method == nil {
		return
	}
	bindResourceReader(method, server)
}

// ResourceTemplate advertises a parameterized URI on the current resource reader.
// All templates in one MCP service must use the same unary method. Its payload
// contains one required uri string. Its result contains contents, an
// ArrayOfRequired of objects with one required content OneOf using text/blob
// branches. Text requires uri and text; blob requires uri and a Bytes blob field.
// Make contents optional if an existing resource can have no contents; required
// contents must declare MinLength(1). The method owns URI interpretation,
// resource existence, and access checks.
// Template expansion never supplies original variable values to the method.
func ResourceTemplate(name, uriTemplate, mimeType string) {
	method, server := mcpMethod()
	if method == nil || !bindResourceReader(method, server) {
		return
	}
	server.ResourceTemplates = append(server.ResourceTemplates, &exprmcp.ResourceTemplateExpr{
		Name: name, URI: uriTemplate, MimeType: mimeType, Description: method.Description,
	})
}

// bindResourceReader gives exact URI reads one owner. Repeated template
// declarations on that method share it; competing methods fail during evaluation.
func bindResourceReader(method *expr.MethodExpr, server *exprmcp.MCPExpr) bool {
	if server.ResourceReader != nil && server.ResourceReader != method {
		eval.ReportError("MCP resource declarations must use the same reader method")
		return false
	}
	server.ResourceReader = method
	return true
}
