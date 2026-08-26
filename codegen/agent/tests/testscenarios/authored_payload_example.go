package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
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
				"query": "recent records",
				"limit": 7,
			})
		})

		var SummarizeResult = Type("SummarizeResult", func() {
			Attribute("summary", String, "Summarized response.")
			Required("summary")
		})

		Service("calc", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("summarize_doc", "Summarize a document", func() {
						aidsl.Args(SummarizePayload)
						aidsl.Return(SummarizeResult)
					})
				})
			})
		})
	}
}

// AuthoredPayloadExampleThroughPrepare returns a DSL design where Prepare hides
// server-injected fields before specs are generated. The generator must preserve
// explicit examples after flattening the user type into the model-facing shape.
func AuthoredPayloadExampleThroughPrepare() func() {
	return func() {
		API("calc", func() {})

		var QueryByName = Type("QueryByName", func() {
			Field(1, "name", String, "Name to search for.")
			Required("name")
		})

		var QueryByID = Type("QueryByID", func() {
			Field(1, "id", String, "Identifier to load.")
			Required("id")
		})

		var LookupPayload = Type("LookupPayload", func() {
			Example(Val{
				"query": Val{
					"type": "by_name",
					"value": Val{
						"name": "record_1",
					},
				},
			})
			Field(1, "session_id", String, "Server-injected session identifier.")
			OneOf("query", func() {
				Field(2, "by_name", QueryByName, "Lookup by name.")
				Field(3, "by_id", QueryByID, "Lookup by ID.")
			})
			Required("session_id", "query")
		})

		var LookupResult = Type("LookupResult", func() {
			Field(1, "ok", Boolean, "Whether the lookup succeeded.")
			Required("ok")
		})

		configureLookupTool := func(args any) {
			aidsl.Args(args, "Lookup parameters.")
			aidsl.Return(LookupResult)
		}

		Service("calc", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("lookup", "Lookup an entity", func() {
						configureLookupTool(LookupPayload)
						aidsl.Inject("session_id")
					})
				})
			})
		})
	}
}
