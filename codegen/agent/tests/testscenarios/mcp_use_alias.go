package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUseAlias references a Goa-defined MCP toolset through a local alias so the
// generator must keep definition-owned package names separate from provider
// metadata.
func MCPUseAlias() func() {
	return func() {
		API("alpha", func() {})
		Service("calc", func() {
			aidsl.MCP("core", "1.0.0")
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				aidsl.Tool("add", "Add two numbers")
			})
		})
		var CalcRemote = aidsl.Toolset("calc-remote", aidsl.FromMCP("calc", "core"))
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use(CalcRemote)
			})
		})
	}
}
