package mcp

import (
	_ "goa.design/goa-ai/codegen" // Import to trigger plugin registration
	mcpexpr "goa.design/goa-ai/expr"
	"goa.design/goa/v3/eval"
)

// Register initializes the MCP plugin
func init() {
	// Initialize the plugin root
	mcpexpr.Root = mcpexpr.NewRoot()

	// Register the root with the eval engine
	_ = eval.Register(mcpexpr.Root)
}
