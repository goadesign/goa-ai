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
		var PurgePayload = Type("PurgePayload", func() {
			Attribute("session_id", String, "Session ID")
			Required("session_id")
			Example(map[string]any{"session_id": "session-123"})
		})
		// Target service with a no-result method.
		Service("tasks", func() {
			Method("purge", func() {
				Payload(PurgePayload)
			})
			Method("heartbeat", func() {})
		})
		// Agent on a different service binds a tool to the tasks.purge method.
		Service("alpha", func() {
			Agent("scribe", "Ops", func() {
				Use("ops", func() {
					Tool("purge", "Purge", func() {
						Args(PurgePayload)
						BindTo("tasks", "purge")
					})
					Tool("heartbeat", "Record a heartbeat", func() {
						BindTo("tasks", "heartbeat")
					})
				})
			})
		})
	}
}

// EmptyPayloadResultMethod returns a bound tool whose service method accepts no
// payload and returns one value.
func EmptyPayloadResultMethod() func() {
	return func() {
		API("alpha", func() {})
		Service("tasks", func() {
			Method("status", func() {
				Result(String)
			})
		})
		Service("alpha", func() {
			Agent("scribe", "Ops", func() {
				Use("ops", func() {
					Tool("status", "Read status", func() {
						BindTo("tasks", "status")
					})
				})
			})
		})
	}
}
