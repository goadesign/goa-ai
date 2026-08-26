package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ToolSpecsMinimal returns a DSL design function for a minimal tool_specs scenario.
func ToolSpecsMinimal() func() {
	return func() {
		API("calc", func() {})
		var SummarizePayload = Type("SummarizePayload", func() {
			Attribute("doc_id", String, "Document identifier")
			Required("doc_id")
		})
		var SummarizeResult = Type("SummarizeResult", func() {
			Attribute("title", String, "Title")
			Required("title")
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
