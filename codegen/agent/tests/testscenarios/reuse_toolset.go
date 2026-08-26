package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ReUse declares a top-level toolset and references it via Use.
func ReUse() func() {
	return func() {
		API("alpha", func() {})
		var Shared = aidsl.Toolset("shared", func() {
			aidsl.Tool("ping", "Ping", func() {})
		})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use(Shared)
			})
		})
	}
}
