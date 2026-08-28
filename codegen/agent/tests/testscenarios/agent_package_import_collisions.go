// Package testscenarios provides Goa designs that exercise complete agent code generation.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AgentPackageImportCollisions defines generated tool packages whose requested
// names match the runtime agent package used by the generated implementation.
func AgentPackageImportCollisions() func() {
	return func() {
		API("agent package import collisions", func() {})
		Service("calc", func() {
			MCP("core", "1.0.0")
			JSONRPC(func() {
				POST("/calc")
			})
			Method("run", func() {
				Result(String)
				Tool("run", "Returns one value.")
			})
		})
		var AgentTools = Toolset("agent", FromMCP("calc", "core"))
		Service("alpha", func() {
			Agent("scribe", "Exercises import name planning.", func() {
				Use(AgentTools)
			})
		})
	}
}
