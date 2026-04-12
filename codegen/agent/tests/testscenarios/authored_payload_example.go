package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AuthoredPayloadExample returns a DSL design where the tool payload defines an
// explicit top-level Example(...) that should be preserved in generated specs.
func AuthoredPayloadExample() func() {
	return func() {
		API("calc", func() {})

		var SummarizePayload = Type("SummarizePayload", func() {
			Attribute("query", String, "Search query.")
			Attribute("limit", Int, "Maximum number of rows.")
			Required("query")
			Example(Val{
				"query": "battery alarms",
				"limit": 7,
			})
		})

		var SummarizeResult = Type("SummarizeResult", func() {
			Attribute("summary", String, "Summarized response.")
			Required("summary")
		})

		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("summarize_doc", "Summarize a document", func() {
						Args(SummarizePayload)
						Return(SummarizeResult)
					})
				})
			})
		})
	}
}
