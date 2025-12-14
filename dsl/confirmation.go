package dsl

import (
	"goa.design/goa/v3/eval"

	agentsexpr "goa.design/goa-ai/expr/agent"
)

// Confirmation declares that the current tool always requires explicit out-of-band
// operator confirmation before execution.
//
// Confirmation must appear inside a Tool DSL in a Toolset.
//
// The runtime enforces confirmation using the ask_question-style protocol:
// a confirmation tool is invoked out-of-band via AwaitExternalTools, and the
// tool is executed only after the user approves it.
func Confirmation(dsl func()) {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Confirmation = &agentsexpr.ToolConfirmationExpr{}
	if dsl != nil {
		eval.Execute(dsl, tool.Confirmation)
	}
}

// PromptTemplate sets the operator-facing prompt template rendered
// during confirmation. The template is executed with the tool payload value.
func PromptTemplate(tmpl string) {
	c, ok := eval.Current().(*agentsexpr.ToolConfirmationExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	c.PromptTemplate = tmpl
}

// DeniedResultTemplate sets the JSON template used to construct a
// schema-compliant tool result when the user denies confirmation. The template
// is executed with the tool payload value and must render valid JSON.
func DeniedResultTemplate(tmpl string) {
	c, ok := eval.Current().(*agentsexpr.ToolConfirmationExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	c.DeniedResultTemplate = tmpl
}
