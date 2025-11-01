package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ReuseToolset declares a top-level toolset and references it from Uses.
func ReuseToolset() func() {
	return func() {
		API("alpha", func() {})
		var Shared = Toolset("shared", func() {
			Tool("ping", "Ping", func() {})
		})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset(Shared)
				})
			})
		})
	}
}
