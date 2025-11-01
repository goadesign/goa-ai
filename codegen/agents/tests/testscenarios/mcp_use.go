package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUseToolset references an external MCP toolset.
func MCPUseToolset() func() {
	return func() {
		API("alpha", func() {})
		// Provider service referenced by UseMCPToolset
		Service("calc", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					UseMCPToolset("calc", "core")
				})
			})
		})
	}
}
