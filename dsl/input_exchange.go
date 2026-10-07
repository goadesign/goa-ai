// Package dsl marks the native service fields that carry an operation's input
// rounds. The method keeps its Goa types; generated tools expose only domain
// arguments and the completed branch while the host supplies continuation data.
package dsl

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// InputExchange binds an optional payload continuation object and a required
// result outcome OneOf to one operation's additional-input rounds. The outcome
// has complete and input_required branches. Only complete enters the model's
// result contract; continuation stays outside its arguments.
//
// Continuation declares optional state and responses fields. The input_required
// branch declares optional state and requests fields. Requests and responses
// have matching optional question identifiers. Each question requests a message
// and either typed form content or a URL; its answer selects accept, decline or
// cancel. Accepted form content supplies the generated form schema.
//
// Use inside a Goa Method exposed as an MCP tool, resource reader or prompt,
// or called by a BindTo tool locally or through a registry provider.
// The service owns state integrity, authorization and domain effects. The runtime and MCP callers
// keep the original arguments and supply host answers for that exact invocation.
func InputExchange(continuation, outcome string) {
	method, ok := eval.Current().(*expr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if continuation == "" || outcome == "" {
		eval.ReportError("InputExchange requires continuation and outcome field names")
		return
	}
	if _, declared := method.Meta[mcpinput.ExchangeMetaKey]; declared {
		eval.ReportError("InputExchange may appear only once per method")
		return
	}
	if method.Meta == nil {
		method.Meta = make(expr.MetaExpr)
	}
	method.Meta[mcpinput.ExchangeMetaKey] = []string{continuation, outcome}
}
