package codegen

import (
	ir "goa.design/goa-ai/codegen/ir"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// CompletionData captures the template-ready metadata for one service-owned
	// typed completion contract.
	CompletionData struct {
		// Name is the DSL identifier.
		Name string
		// Description is the DSL description.
		Description string
		// GoName is the exported Go identifier derived from Name.
		GoName string
		// Result is the typed assistant-output contract.
		Result *goaexpr.AttributeExpr
	}
)

// newCompletionDataFromIR transforms a canonical IR completion into the
// template-ready metadata used by service-owned completion generation.
func newCompletionDataFromIR(completion *ir.Completion) *CompletionData {
	if completion == nil || completion.Expr == nil {
		return nil
	}
	return &CompletionData{
		Name:        completion.Name,
		Description: completion.Description,
		GoName:      completion.GoName,
		Result:      completion.Expr.Return,
	}
}
