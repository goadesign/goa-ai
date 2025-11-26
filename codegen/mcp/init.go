package codegen

import (
	goacodegen "goa.design/goa/v3/codegen"
)

// Register MCP code generation plugins with Goa.
// This ensures the plugin hooks run during both generation and example scaffolding.
func init() {
	goacodegen.RegisterPluginFirst("mcp", "gen", PrepareServices, Generate)
	goacodegen.RegisterPlugin("mcp", "example", PrepareExample, ModifyExampleFiles)
	goacodegen.RegisterPluginLast("mcp-cli", "gen", nil, PatchCLIToUseMCPAdapter)
}
