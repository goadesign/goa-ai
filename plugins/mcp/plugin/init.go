package plugin

import (
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

func init() {
	codegen.RegisterPlugin("gen", "mcp", Prepare, Generate)
}

// Prepare runs before generation. No-op for now.
func Prepare(_ string, _ []eval.Root) error { return nil }

// Generate inspects the design roots and augments generated files with MCP server code.
func Generate(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	g := NewGenerator(genpkg, roots, files)
	return g.Run()
}