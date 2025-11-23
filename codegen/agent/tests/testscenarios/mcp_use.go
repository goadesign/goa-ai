package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUse references an external MCP toolset.
func MCPUse() func() {
	return func() {
		API("alpha", func() {})
		// Provider service referenced by MCPToolset
		Service("calc", func() {})
		var CalcCore = MCPToolset("calc", "core")
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcCore)
			})
		})
	}
}
