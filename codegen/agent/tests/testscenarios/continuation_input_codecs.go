package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ContinuationInputCodecs declares a query whose continuation retains its
// original arguments and a cursor-only continuation that needs no retained query.
func ContinuationInputCodecs() func() {
	return func() {
		API("alpha", func() {})
		query := Type("Query", func() {
			Attribute("query", String, "Text to search.")
			Attribute("cursor", String, "Position supplied by the runtime.")
			Attribute("session_id", String, "Session supplied by the provider.")
			Required("query", "session_id")
			Example(map[string]any{"query": "records", "session_id": "session"})
		})
		Service("alpha", func() {
			Agent("scribe", "Searches records.", func() {
				Use("lookup", func() {
					Tool("list", "List records.", func() {
						Args(func() {
							Attribute("kind", String, "Record kind.")
							Required("kind")
							Example(map[string]any{"kind": "notes"})
						})
						Return(func() {
							Attribute("records", ArrayOf(String), "Matching records.")
						})
						BoundedResult(func() {
							ContinueWith("continue_list", "nextPosition")
							NextCursor("next_cursor")
						})
					})
					Tool("continue_list", "Continue listing records.", func() {
						Args(func() {
							Attribute("nextPosition", String, "Position supplied by the runtime.")
							Required("nextPosition")
							Example(map[string]any{"nextPosition": "page"})
						})
						Return(func() {
							Attribute("records", ArrayOf(String), "Matching records.")
						})
						BoundedResult(func() {
							Cursor("nextPosition")
							NextCursor("next_cursor")
						})
					})
					Tool("search_manual", "Search with an explicitly supplied position.", func() {
						Args(query)
						Inject("session_id")
						Return(func() {
							Attribute("records", ArrayOf(String), "Matching records.")
						})
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
					Tool("search", "Search records.", func() {
						Args(query)
						Inject("session_id")
						Return(func() {
							Attribute("records", ArrayOf(String), "Matching records.")
						})
						BoundedResult(func() {
							ContinueWith("continue_search", "cursor")
							NextCursor("next_cursor")
						})
					})
					Tool("continue_search", "Continue a search.", func() {
						Args(query)
						Inject("session_id")
						Return(func() {
							Attribute("records", ArrayOf(String), "Matching records.")
						})
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
