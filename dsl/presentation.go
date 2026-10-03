// Package dsl declares which tools and arguments need an interactive client.
// Code generation prepares separate model contracts before any run starts.
package dsl

import (
	agentexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// RequiresUI excludes a tool from text-only runs because executing it requires
// rendering, structured input, or another interactive client protocol.
func RequiresUI() {
	tool, ok := eval.Current().(*agentexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.RequiresUI = true
}

// UIOnly hides optional Boolean arguments from text-only model contracts.
// The generated decoder rejects these arguments and supplies false to execution.
func UIOnly(fields ...string) {
	tool, ok := eval.Current().(*agentexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.UIOnlyFields = append(tool.UIOnlyFields, fields...)
}

// UIInstructions adds optional rendering guidance to the ordinary tool
// description. Text-only model descriptions and search documents omit it.
func UIInstructions(text string) {
	tool, ok := eval.Current().(*agentexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.UIInstructions = text
}
