package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelfBoundedResult returns a DSL design function for a
// method-backed bounded tool whose semantic result stays domain-only while the
// bound method result carries canonical bounds fields for projection.
func ServiceToolsetBindSelfBoundedResult() func() {
	return serviceToolsetBindSelfBoundedResult(false)
}

// ServiceToolsetBindSelfBoundedResultExactTotal returns the same bounded tool
// with total strengthened to a required exact cardinality.
func ServiceToolsetBindSelfBoundedResultExactTotal() func() {
	return serviceToolsetBindSelfBoundedResult(true)
}

func serviceToolsetBindSelfBoundedResult(exactTotal bool) func() {
	return func() {
		API("alpha", func() {})
		var SearchPayload = Type("SearchPayload", func() {
			Attribute("query", String, "Query")
			Attribute("cursor", String, "Cursor")
			Required("query")
			Example(map[string]any{"query": "records"})
		})
		var SearchResult = Type("SearchResult", func() {
			Attribute("results", ArrayOf(String), "Results")
			Required("results")
			Example(map[string]any{"results": []string{"record_2"}})
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
					if exactTotal {
						Required("results", "returned", "total", "truncated")
					} else {
						Required("results", "returned", "truncated")
					}
				})
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("lookup", func() {
					aidsl.Tool("search", "Search", func() {
						aidsl.Args(SearchPayload)
						aidsl.Return(SearchResult)
						aidsl.BindTo("alpha", "Search")
						aidsl.BoundedResult(func() {
							aidsl.Cursor("cursor")
							aidsl.NextCursor("next_cursor")
						})
					})
					if !exactTotal {
						aidsl.Tool("search_copy", "Search with the same limits", func() {
							aidsl.Args(SearchPayload)
							aidsl.Return(SearchResult)
							aidsl.BindTo("alpha", "Search")
							aidsl.BoundedResult(func() {
								aidsl.Cursor("cursor")
								aidsl.NextCursor("next_cursor")
							})
						})
						aidsl.Tool("search_all", "Search without generated limits", func() {
							aidsl.Args(SearchPayload)
							aidsl.Return(SearchResult)
						})
					}
				})
			})
		})
	}
}
