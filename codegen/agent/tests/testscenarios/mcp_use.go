package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MCPUse references a Goa-defined MCP toolset using Toolset with FromMCP.
func MCPUse() func() {
	return mcpUse(true)
}

// MCPUseNoResult references a Goa-defined MCP tool whose method returns only
// an error, so generated executors must not decode a result value.
func MCPUseNoResult() func() {
	return mcpUse(false)
}

// MCPUseExternalInlineInject references an external MCP toolset whose inline
// payload hides one field from the model and fills it from tool-call metadata.
func MCPUseExternalInlineInject() func() {
	return func() {
		API("alpha", func() {})
		Service("remote", func() {})
		var RemoteSearch = Toolset("remote-search", FromExternalMCP("remote", "search"), func() {
			Tool("lookup", "Look up a remote record", func() {
				Args(func() {
					Attribute("session_id", String, "Server-injected session identifier.")
					Attribute("query", String, "Search query.")
					Required("session_id", "query")
				})
				Return(String)
				Inject("session_id")
			})
		})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use(RemoteSearch)
			})
		})
	}
}

// mcpUse builds the shared Goa-backed MCP fixture with or without a method
// result so compile tests exercise both generated executor branches.
func mcpUse(hasResult bool) func() {
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
				if hasResult {
					Result(Int)
				}
				Tool("add", "Add two numbers")
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
