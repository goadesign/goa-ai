package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// NoResultMethod returns a DSL design with a method-backed tool whose target
// service method returns only an error (no result). Tests assert service_toolset
// code generation handles no-result methods correctly.
func NoResultMethod() func() {
	return func() {
		API("alpha", func() {})
		// Target service with a no-result method.
		Service("tasks", func() {
			Method("purge", func() {
				Payload(func() {
					Attribute("session_id", String, "Session ID")
					Required("session_id")
				})
			})
		})
		// Agent on a different service binds a tool to the tasks.purge method.
		Service("alpha", func() {
			Agent("scribe", "Ops", func() {
				Use("ops", func() {
					Tool("purge", "Purge", func() {
						BindTo("tasks", "purge")
					})
				})
			})
		})
	}
}
