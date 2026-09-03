package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsSimple declares an agent that exports a single toolset with one tool.
func ExportsSimple() func() {
	return func() {
		API("alpha", func() {})
		var FindInput = Type("FindInput", func() {
			Attribute("query", String, "Document query")
			Required("query")
		})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Export("search", func() {
					Tool("find", "Find documents", func() {
						Args(FindInput)
						Return(String)
					})
				})
			})
		})
	}
}
