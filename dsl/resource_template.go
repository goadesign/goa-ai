// Package dsl declares parameterized resource catalogs. The service receives the
// client's URI unchanged and owns finding and authorizing its contents.
package dsl

import (
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

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
	method, ok := eval.Current().(*expr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	server := exprmcp.Root.GetMCP(method.Service)
	if server == nil {
		eval.IncompatibleDSL()
		return
	}
	server.ResourceTemplates = append(server.ResourceTemplates, &exprmcp.ResourceTemplateExpr{
		Name: name, URI: uriTemplate, MimeType: mimeType, Description: method.Description, Method: method,
	})
}
