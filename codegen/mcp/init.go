// This file registers MCP code generation with Goa.
package codegen

import (
	goagenerator "goa.design/goa/v3/codegen/generator"
)

// Register both normal generation and example generation.
func init() {
	goagenerator.RegisterPluginFirst("mcp", "gen", newMCPPlugin)
	goagenerator.RegisterPlugin("mcp", "example", newMCPExamplePlugin)
}
