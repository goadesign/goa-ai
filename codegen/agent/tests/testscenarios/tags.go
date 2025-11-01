package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TagsBasic returns a DSL design with a tool exposing tags.
func TagsBasic() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("helpers", func() {
						Tool("summarize", "Summarize a document", func() {
							Tags("nlp", "summarization")
						})
					})
				})
			})
		})
	}
}
