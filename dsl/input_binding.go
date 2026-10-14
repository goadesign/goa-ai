// Package dsl declares required provider fields supplied by the consuming application.
// Generation omits those fields from model input and creates functions that combine
// application values with model arguments without changing the provider contract.
package dsl

import (
	"slices"

	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// BindInput asks generation to omit required provider fields from this consumer's
// model input and generate a helper that accepts their typed values. The application
// installs that helper for each planning activity; other consumers still see the
// original provider input. Unknown, optional, injected, and repeated fields fail
// generation. Fields name top-level attributes, not paths into nested values.
//
// BindInput must appear inside Use of a known toolset definition. Remote registry
// contracts cannot be bound without a compiled provider declaration.
//
//	Use(Records, func() {
//	    BindInput("read", "scope")
//	})
func BindInput(tool string, fields ...string) {
	current, ok := eval.Current().(*agentsexpr.ToolsetExpr)
	if !ok || current.Agent == nil || current.Agent.Used == nil ||
		!slices.Contains(current.Agent.Used.Toolsets, current) || current.Origin == nil {
		eval.ReportError("BindInput must appear inside Use of a known toolset definition")
		return
	}
	if current.Provider != nil && current.Provider.Kind == agentsexpr.ProviderRegistry {
		eval.ReportError("BindInput requires a compiled provider declaration; remote Registry contracts are unknown during generation")
		return
	}
	if tool == "" || len(fields) == 0 {
		eval.ReportError("BindInput requires a tool name and at least one required field")
		return
	}
	for _, binding := range current.InputBindings {
		if binding.Tool == tool {
			eval.ReportError("BindInput tool %q is repeated; list its fields in one declaration", tool)
			return
		}
	}
	for i, field := range fields {
		if field == "" {
			eval.ReportError("BindInput field name cannot be empty")
			return
		}
		if slices.Contains(fields[:i], field) {
			eval.ReportError("BindInput field %q is repeated", field)
			return
		}
	}
	current.InputBindings = append(current.InputBindings, &agentsexpr.InputBindingExpr{
		Tool:   tool,
		Fields: slices.Clone(fields),
	})
}
