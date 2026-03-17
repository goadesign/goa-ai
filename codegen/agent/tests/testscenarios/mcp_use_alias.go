package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUseAlias references an external MCP toolset through a local alias so the
// generator must keep definition-owned package names separate from provider
// metadata.
func MCPUseAlias() func() {
	return func() {
		API("alpha", func() {})
		Service("calc", func() {})
		var CalcRemote = Toolset("calc-remote", FromMCP("calc", "core"))
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcRemote)
			})
		})
	}
}
