package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ImportsDeterministic uses a user type with a custom package path to exercise alias stability.
func ImportsDeterministic() func() {
	return func() {
		API("alpha", func() {})
		var Doc = Type("Doc", func() {
			Meta("struct:pkg:path", "example.com/mod/gen/types")
			Attribute("id", String, "Identifier")
			Required("id")
		})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("docs", func() {
					aidsl.Tool("store", "Store", func() {
						aidsl.Args(Doc)
						aidsl.Return(Doc)
					})
				})
			})
		})
	}
}
