package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportedToolNameCollisions declares tool names that collide with fixed
// declarations and with each other after Go identifier conversion.
func ExportedToolNameCollisions() func() {
	return func() {
		API("agent_tool_names", func() {})
		Service("alpha", func() {
			Agent("scribe", "Writes documents", func() {
				Export("helpers", func() {
					Tool("agent-id", "Find an agent by ID", func() {
						Args(String)
						Return(String)
					})
					Tool("agent_id", "Load an agent by ID", func() {
						Args(String)
						Return(String)
					})
					Tool("service", "Describe a service", func() {
						Args(String)
						Return(String)
					})
				})
			})
		})
	}
}
