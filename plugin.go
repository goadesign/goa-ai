package mcp

import (
	"goa.design/goa/v3/eval"
	_ "goa.design/plugins/v3/mcp/codegen" // Import to trigger plugin registration
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

// Register initializes the MCP plugin
func init() {
	// Initialize the plugin root
	mcpexpr.Root = mcpexpr.NewRoot()

	// Register the root with the eval engine
	_ = eval.Register(mcpexpr.Root)
}
