// Package testscenarios supplies independently understandable tool designs for
// generated model and execution contract tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TextOnlyContinuationInputCodecs declares a source and continuation that share
// optional UI controls while the runtime owns their cursor.
func TextOnlyContinuationInputCodecs() func() {
	return func() {
		API("alpha", func() {})
		input := Type("ReadInput", func() {
			Attribute("query", String, "Text to search.")
			Attribute("cursor", String, "Position supplied by the runtime.")
			Attribute("renderUi", Boolean, "Show an interactive card.")
			Attribute("session_id", String, "Session supplied by the provider.")
			Required("query", "session_id")
		})
		result := Type("ReadResult", func() {
			Attribute("count", Int, "Number of records in this page.")
			Required("count")
		})
		Service("alpha", func() {
			Agent("reader", "Reads record pages.", func() {
				Use("lookup", func() {
					Tool("read", "Read the first page.", func() {
						Args(input)
						Return(result)
						Inject("session_id")
						UIOnly("renderUi")
						BoundedResult(func() {
							ContinueWith("continue_read", "cursor")
							NextCursor("next_cursor")
						})
					})
					Tool("continue_read", "Read the next page.", func() {
						Args(input)
						Return(result)
						Inject("session_id")
						UIOnly("renderUi")
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
