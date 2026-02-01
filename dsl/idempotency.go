package dsl

import (
	"goa.design/goa/v3/eval"

	agentsexpr "goa.design/goa-ai/expr/agent"
	runtimetools "goa.design/goa-ai/runtime/agent/tools"
)

// Idempotent marks a tool as idempotent within a run transcript.
//
// When set, orchestration layers may treat repeated tool calls with identical
// arguments as redundant and avoid executing them once a successful result
// already exists in the transcript.
//
// Use Idempotent only for tools whose result is a pure function of their
// arguments *for the lifetime of a run transcript*. If a tool answers questions
// about changing state (for example, "current mode") but does not accept a time
// or version parameter, it is not transcript-idempotent and should not be
// tagged.
//
// Default: tools are not idempotent across a transcript unless explicitly tagged.
func Idempotent() {
	tool, ok := eval.Current().(*agentsexpr.ToolExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	tool.Tags = append(tool.Tags, runtimetools.TagIdempotencyTranscript)
}
