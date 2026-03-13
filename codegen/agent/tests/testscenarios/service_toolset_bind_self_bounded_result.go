package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelfBoundedResult returns a DSL design function for a
// method-backed bounded tool whose semantic result stays domain-only while the
// bound method result carries canonical bounds fields for projection.
func ServiceToolsetBindSelfBoundedResult() func() {
	return func() {
		API("alpha", func() {})
		var SearchPayload = Type("SearchPayload", func() {
			Attribute("query", String, "Query")
			Attribute("cursor", String, "Cursor")
			Required("query")
		})
		var SearchResult = Type("SearchResult", func() {
			Attribute("results", ArrayOf(String), "Results")
			Required("results")
		})
		Service("alpha", func() {
			Method("Search", func() {
				Payload(func() {
					Attribute("query", String, "Query")
					Attribute("cursor", String, "Cursor")
					Required("query")
				})
				Result(func() {
					Attribute("results", ArrayOf(String), "Results")
					Attribute("returned", Int, "Returned count")
					Attribute("total", Int, "Total count")
					Attribute("truncated", Boolean, "Truncation flag")
					Attribute("next_cursor", String, "Next cursor")
					Attribute("refinement_hint", String, "Refinement hint")
					Required("results", "returned", "truncated")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("search", "Search", func() {
						Args(SearchPayload)
						Return(SearchResult)
						BindTo("alpha", "Search")
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
				})
			})
		})
	}
}
