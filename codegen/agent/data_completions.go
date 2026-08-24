// This file stores the completion information read by generated completion
// packages and example commands.
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
		// ConstName is the final generated constant name.
		ConstName string
		// SpecVar is the final generated specification variable name.
		SpecVar string
		// CompleteFunc is the final generated completion function name.
		CompleteFunc string
		// StreamFunc is the final generated streaming completion function name.
		StreamFunc string
		// Result is the typed assistant-output contract.
		Result *goaexpr.AttributeExpr
		// DecodeChunkFunc names the generated function that reads the final stream value.
		DecodeChunkFunc string
	}
)

// newCompletionDataFromIR reads one saved completion definition and returns the
// values used to write its generated files.
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

