//go:build goa_mcp_plugin

package mcp

import (
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// Tool marks a method as an MCP tool and allows setting metadata via nested functions.
func Tool(fn ...func()) {
	if _, ok := eval.Current().(*expr.MethodExpr); !ok {
		eval.IncompatibleDSL()
		return
	}
	md := ensureMetadata("mcp:tool")
	_ = md
	for _, f := range fn {
		f()
	}
}

// Resource marks a method as providing MCP resources (list/get) semantics.
func Resource(fn ...func()) {
	if _, ok := eval.Current().(*expr.MethodExpr); !ok {
		eval.IncompatibleDSL()
		return
	}
	_ = ensureMetadata("mcp:resource")
	for _, f := range fn {
		f()
	}
}

// Prompt marks a method as an MCP prompt provider.
func Prompt(fn ...func()) {
	if _, ok := eval.Current().(*expr.MethodExpr); !ok {
		eval.IncompatibleDSL()
		return
	}
	_ = ensureMetadata("mcp:prompt")
	for _, f := range fn {
		f()
	}
}

// Description sets a short description for the annotated MCP element.
func Description(text string) {
	switch cur := eval.Current().(type) {
	case *expr.MethodExpr:
		if cur.Meta == nil {
			cur.Meta = make(expr.MetaExpr)
		}
		cur.Meta["mcp:description"] = []string{text}
	default:
		eval.IncompatibleDSL()
	}
}

func ensureMetadata(key string) expr.MetaExpr {
	m, _ := eval.Current().(*expr.MethodExpr)
	if m == nil {
		return nil
	}
	if m.Meta == nil {
		m.Meta = make(expr.MetaExpr)
	}
	m.Meta[key] = []string{"true"}
	return m.Meta
}