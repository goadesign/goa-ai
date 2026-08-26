package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsSimple declares an agent that exports a single toolset with one tool.
func ExportsSimple() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Export("search", func() {
					aidsl.Tool("find", "Find documents", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
				})
			})
		})
	}
}
