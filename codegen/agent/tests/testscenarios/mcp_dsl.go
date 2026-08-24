package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPDSL references a Goa-defined MCP toolset using the Toolset with FromMCP DSL.
func MCPDSL() func() {
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
		var CalcCore = aidsl.Toolset(aidsl.FromMCP("calc", "core"))
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use(CalcCore)
			})
		})
	}
}
