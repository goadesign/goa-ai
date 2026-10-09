// Package dsl binds typed Goa suggestion methods to known MCP prompt arguments.
// The generator owns protocol routing; application methods own the suggestions.
package dsl

import (
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// PromptCompletion exposes the current method as the suggestion provider for
// one declared prompt argument. Its payload contains required value: String and
// optional arguments: MapOf(String, String). Its result contains values:
// ArrayOf(String) with MaxLength(100), and optional total: Int64 and hasMore:
// Boolean. An optional values field permits an empty suggestion list.
//
// PromptCompletion appears inside a Method of an MCP service. It does not call
// the prompt method or a model. The service returns suggestions in relevance
// order and enforces the caller's current access to those suggestions.
func PromptCompletion(prompt, argument string) {
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
	server.PromptCompletions = append(server.PromptCompletions, &mcpexpr.PromptCompletionExpr{Prompt: prompt, Argument: argument, Method: method})
}
