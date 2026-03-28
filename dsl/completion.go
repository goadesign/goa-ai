package dsl

import (
	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Completion declares a service-owned typed assistant-output contract.
//
// A completion belongs to the surrounding Goa service and declares the schema of
// a direct assistant response, as opposed to a tool call result. Code generation
// emits the completion schema, JSON codec, and typed runtime helpers under the
// generated service completions package.
//
// Completion must appear inside a Service expression.
//
// Completion accepts:
//   - name: the completion identifier
//   - description: an optional human-readable description
//   - dsl: an optional block that configures the completion contract
//
// Inside the DSL block, use:
//   - Return: defines the typed assistant output schema
//
// Example:
//
//	var Draft = Type("Draft", func() {
//	    Attribute("name", String, "Task name")
//	    Attribute("goal", String, "Outcome-style goal")
//	    Required("name", "goal")
//	})
//
//	Service("tasks", func() {
//	    Completion("draft_from_transcript", "Structured task draft synthesis", func() {
//	        Return(Draft)
//	    })
//	})
func Completion(name string, args ...any) *expragents.CompletionExpr {
	var description string
	var dslf func()

	if name == "" {
		eval.ReportError("completion name cannot be empty")
		return nil
	}
	svc, ok := eval.Current().(*goaexpr.ServiceExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	for _, arg := range args {
		switch actual := arg.(type) {
		case string:
			description = actual
		case func():
			dslf = actual
		default:
			eval.InvalidArgError("string or function", arg)
			return nil
		}
	}
	completion := &expragents.CompletionExpr{
		Name:        name,
		Description: description,
		Service:     svc,
		DSLFunc:     dslf,
	}
	expragents.Root.Completions = append(expragents.Root.Completions, completion)
	return completion
}
