// Package testscenarios defines reusable Goa and goa-ai designs for generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// InjectReusableExportExample defines one shared toolset that a service exports
// and another service consumes. One tool inherits its input from a bound method;
// the other declares the same input explicitly.
func InjectReusableExportExample() func() {
	return func() {
		API("shared-tools", func() {})
		var ExplicitArgs = Type("ExplicitArgs", func() {
			Attribute("session_id", String, "Server-injected session identifier.")
			Attribute("query", String, "Search query.")
			Required("session_id", "query")
		})
		var Helpers = Toolset("helpers", func() {
			Tool("inherited", "Use the bound method input", func() {
				BindTo("atlas", "inherited")
				Inject("session_id")
			})
			Tool("explicit", "Use the declared tool input", func() {
				Args(ExplicitArgs)
				BindTo("atlas", "explicit")
				Inject("session_id")
			})
		})
		Service("atlas", func() {
			Method("inherited", func() {
				Payload(func() {
					Attribute("session_id", String, "Server-injected session identifier.")
					Attribute("query", String, "Search query.")
					Required("session_id", "query")
				})
				Result(String)
			})
			Method("explicit", func() {
				Payload(ExplicitArgs)
				Result(String)
			})
			Export(Helpers)
		})
		Service("chat", func() {
			Agent("assistant", "Shared tool consumer", func() {
				Use(Helpers)
			})
		})
	}
}
