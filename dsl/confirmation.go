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
// The runtime enforces confirmation using its runtime-owned confirmation
// transport: it publishes await_confirmation and executes the tool only after
// the user approves it.
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
// during confirmation. The template reads canonical JSON argument names, such
// as {{ .device_alias }}, and never depends on generated Go field names.
// Use index for optional JSON properties. The json function preserves exact
// JSON values when inserting an argument into the prompt.
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
// reads canonical JSON argument names and must render valid JSON. Use the json
// function to insert argument values without manual quoting.
func DeniedResultTemplate(tmpl string) {
	c, ok := eval.Current().(*agentsexpr.ToolConfirmationExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	c.DeniedResultTemplate = tmpl
}
