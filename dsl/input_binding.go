// Package dsl declares provider fields supplied or resolved by the consuming application.
// Generation omits or replaces those fields in model input and creates functions that
// combine application values with model arguments without changing the provider contract.
package dsl

import (
	"slices"

	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/internal/dslshape"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
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
	current, ok := validateUseToolsetBinding("BindInput")
	if !ok {
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

// BindField binds a top-level field across every tool in the consumed toolset whose
// argument object declares that field. Without Lookup, generation omits the field
// from model input and generates toolset-level and per-tool helpers that inject one
// application-supplied value. When Lookup is declared inside fn, generation also
// emits lookup helpers that expose the declared key attribute to the model and
// resolve the model's key through a caller-supplied lookup table at decode time.
//
//	Use(Records, func() {
//	    BindField("scope", func() {
//	        Lookup(String, "Exact scope name.", func() {
//	            MinLength(1)
//	            Example("North")
//	        })
//	    })
//	})
func BindField(field string, fn ...func()) {
	current, ok := validateUseToolsetBinding("BindField")
	if !ok {
		return
	}
	if field == "" {
		eval.ReportError("BindField field name cannot be empty")
		return
	}
	if len(fn) > 1 {
		eval.TooManyArgError()
		return
	}
	for _, existing := range current.FieldBindings {
		if existing.Field == field {
			eval.ReportError("BindField field %q is repeated", field)
			return
		}
	}
	binding := &agentsexpr.FieldBindingExpr{Field: field}
	if len(fn) == 1 && fn[0] != nil {
		eval.Execute(fn[0], binding)
	}
	current.FieldBindings = append(current.FieldBindings, binding)
}

// Lookup declares a model-facing string key representation for the surrounding
// BindField declaration. At runtime, the application supplies a map from the
// string key to the provider's typed field value; the generated decoder validates
// the model-supplied key and resolves it against that table before preparing the
// provider payload.
func Lookup(keyType any, args ...any) {
	binding, ok := eval.Current().(*agentsexpr.FieldBindingExpr)
	if !ok {
		eval.ReportError("Lookup must appear inside BindField")
		return
	}
	if binding.Lookup != nil {
		eval.ReportError("Lookup can only be declared once per BindField")
		return
	}
	att := dslshape.Build(binding.Field, "lookup", keyType, args...)
	if att == nil {
		return
	}
	if att.Type != goaexpr.String {
		eval.ReportError("Lookup key type must be String")
		return
	}
	binding.Lookup = att
}

func validateUseToolsetBinding(caller string) (*agentsexpr.ToolsetExpr, bool) {
	current, ok := eval.Current().(*agentsexpr.ToolsetExpr)
	if !ok || current.Agent == nil || current.Agent.Used == nil ||
		!slices.Contains(current.Agent.Used.Toolsets, current) || current.Origin == nil {
		eval.ReportError("%s must appear inside Use of a known toolset definition", caller)
		return nil, false
	}
	if current.Provider != nil && current.Provider.Kind == agentsexpr.ProviderRegistry {
		eval.ReportError("%s requires a compiled provider declaration; remote Registry contracts are unknown during generation", caller)
		return nil, false
	}
	return current, true
}
