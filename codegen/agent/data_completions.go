package codegen

import (
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
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

// newCompletionData transforms an evaluated CompletionExpr into template-ready
// metadata for service-owned completion generation.
func newCompletionData(expr *agentsExpr.CompletionExpr) *CompletionData {
	goName := codegen.Goify(expr.Name, true)
	return &CompletionData{
		Name:        expr.Name,
		Description: expr.Description,
		GoName:      goName,
		Result:      expr.Return,
	}
}
