package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsSimple declares an agent that exports a single toolset with one tool.
func ExportsSimple() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Exports(func() {
					Toolset("search", func() {
						Tool("find", "Find documents", func() {
							Args(String)
							Return(String)
						})
					})
				})
			})
		})
	}
}
