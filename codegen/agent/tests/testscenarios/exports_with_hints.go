package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsWithHints declares an exported toolset whose tool configures call and
// result hint templates.
func ExportsWithHints() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Export("search", func() {
					Tool("find", "Find documents", func() {
						Args(func() {
							Attribute("query", String, "Query")
							Required("query")
						})
						Return(func() {
							Attribute("count", Int, "Count")
							Required("count")
						})
						CallHintTemplate("Searching for {{ .Query }}")
						ResultHintTemplate("Found {{ .Result.Count }}")
					})
				})
			})
		})
	}
}
