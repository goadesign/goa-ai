// Package dsl binds URI-template variables to ordinary typed suggestion methods.
// The service owns suggestion relevance and access; generation owns references.
package dsl

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// ResourceCompletion binds the current method to one declared URI-template
// variable. Its payload and result follow PromptCompletion's typed contract:
// required value, optional arguments, and at most 100 ordered suggestion values
// per reply with optional total and hasMore. The client references the exact
// declared template; the method does not read the resource or expand its URI.
func ResourceCompletion(uriTemplate, variable string) {
	method, ok := eval.Current().(*expr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	server := mcpexpr.Root.GetMCP(method.Service)
	if server == nil {
		eval.IncompatibleDSL()
		return
	}
	server.ResourceCompletions = append(server.ResourceCompletions, &mcpexpr.ResourceCompletionExpr{
		URI: uriTemplate, Argument: variable, Method: method,
	})
}
