package dsl

import (
	// Register goa-ai code generation plugins from the single public DSL import
	// path so plugin bootstrap is centralized and explicit.
	_ "goa.design/goa-ai/codegen/agent"
	_ "goa.design/goa-ai/codegen/mcp"
)
