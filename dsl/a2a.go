package dsl

import (
	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// A2A configures A2A-specific settings for an exported toolset.
// It must appear inside an Export toolset DSL. Within the A2A block,
// use Suite, A2APath, and A2AVersion to override defaults.
func A2A(fn ...func()) {
	ts, ok := eval.Current().(*agentsexpr.ToolsetExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if ts.A2A == nil {
		ts.A2A = &agentsexpr.A2AExpr{}
	}
	if len(fn) == 0 || fn[0] == nil {
		return
	}
	eval.Execute(fn[0], ts.A2A)
}

// Suite overrides the default A2A suite identifier for the current toolset.
// It must appear inside an A2A block.
func Suite(id string) {
	a2a, ok := eval.Current().(*agentsexpr.A2AExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	a2a.Suite = id
}

// A2APath overrides the default A2A HTTP path ("/a2a") for the current toolset.
// It must appear inside an A2A block.
func A2APath(path string) {
	a2a, ok := eval.Current().(*agentsexpr.A2AExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	a2a.Path = path
}

// A2AVersion overrides the default A2A protocol version ("1.0") for the
// current toolset. It must appear inside an A2A block.
func A2AVersion(version string) {
	a2a, ok := eval.Current().(*agentsexpr.A2AExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	a2a.Version = version
}


