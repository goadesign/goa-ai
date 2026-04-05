package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUseAlias references a Goa-defined MCP toolset through a local alias so the
// generator must keep definition-owned package names separate from provider
// metadata.
func MCPUseAlias() func() {
	return func() {
		API("alpha", func() {})
		Service("calc", func() {
			MCP("core", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("add", "Add two numbers")
			})
		})
		var CalcRemote = Toolset("calc-remote", FromMCP("calc", "core"))
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcRemote)
			})
		})
	}
}
