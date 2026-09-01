package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUse references a Goa-defined MCP toolset using Toolset with FromMCP.
func MCPUse() func() {
	return func() {
		API("alpha", func() {
			Version("1.2.3")
		})
		Service("calc", func() {
			MCP("core", "1.0.0", ProtocolVersion("2025-06-18"))
			JSONRPC(func() {
				POST("/calc")
			})
			Method("add", func() {
				Payload(func() {
					Attribute("a", Int, "First operand")
					Attribute("b", Int, "Second operand")
					Required("a", "b")
				})
				Result(Int)
				Tool("add", "Add two numbers")
			})
			Method("describe", func() {
				Result(func() {
					Attribute("sum", Int, "Computed sum")
					Required("sum")
				})
				Tool("describe", "Describe the latest sum")
			})
			Method("label", func() {
				Result(String)
				Tool("label", "Return the latest sum label")
			})
			Method("reset", func() {
				Tool("reset", "Clear the latest sum")
			})
		})
		var CalcCore = Toolset(FromMCP("calc", "core"))
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use(CalcCore)
			})
		})
	}
}
