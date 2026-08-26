package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportedToolNameCollisions declares tool names that collide with fixed
// declarations and with each other after Go identifier conversion.
func ExportedToolNameCollisions() func() {
	return func() {
		API("agent_tool_names", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Writes documents", func() {
				aidsl.Export("helpers", func() {
					aidsl.Tool("agent-id", "Find an agent by ID", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
					aidsl.Tool("agent_id", "Load an agent by ID", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
					aidsl.Tool("service", "Describe a service", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
				})
			})
		})
	}
}
