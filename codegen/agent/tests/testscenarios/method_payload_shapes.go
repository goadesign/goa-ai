// Package testscenarios contains complete Goa designs used by generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MethodPayloadShapes returns method-backed tools whose payloads are a
// primitive, an array, and a map.
func MethodPayloadShapes() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Method("Echo", func() {
				Payload(String)
				Result(String)
			})
			Method("Join", func() {
				Payload(ArrayOf(String))
				Result(String)
			})
			Method("Format", func() {
				Payload(MapOf(String, Int))
				Result(String)
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("echo", "Echo text", func() {
						Args(String, "Text to echo")
						Return(String, "Echoed text")
						BindTo("Echo")
					})
					Tool("join", "Join text", func() {
						Args(ArrayOf(String), "Text to join")
						Return(String, "Joined text")
						BindTo("Join")
					})
					Tool("format", "Format numbers", func() {
						Args(MapOf(String, Int), "Numbers to format")
						Return(String, "Formatted numbers")
						BindTo("Format")
					})
				})
			})
		})
	}
}
