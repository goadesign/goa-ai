package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPDSL references an external MCP toolset using the MCPToolset DSL.
func MCPDSL() func() {
	return func() {
		API("alpha", func() {})
		// Provider service referenced by MCPToolset
		Service("calc", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					MCPToolset("calc", "core")
				})
			})
		})
	}
}
