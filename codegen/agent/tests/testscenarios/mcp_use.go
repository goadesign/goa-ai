package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUseToolset references an external MCP toolset.
func MCPUseToolset() func() {
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
