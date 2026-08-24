package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsWithHints declares an exported toolset whose tool configures call and
// result hint templates.
func ExportsWithHints() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Export("search", func() {
					aidsl.Tool("find", "Find documents", func() {
						aidsl.Args(func() {
							Attribute("query", String, "Query")
							Required("query")
						})
						aidsl.Return(func() {
							Attribute("count", Int, "Count")
							Required("count")
						})
						aidsl.CallHintTemplate("Searching for {{ .Query }}")
						aidsl.ResultHintTemplate("Found {{ .Result.Count }}")
					})
				})
			})
		})
	}
}
