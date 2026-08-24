package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TagsBasic returns a DSL design with a tool exposing tags.
func TagsBasic() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("summarize", "Summarize a document", func() {
						aidsl.Tags("nlp", "summarization")
					})
				})
			})
		})
	}
}
