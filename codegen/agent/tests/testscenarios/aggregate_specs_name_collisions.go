// Package testscenarios contains Goa designs used by full agent generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AggregateSpecsNameCollisions declares toolsets whose package names match the
// runtime packages imported by the agent's aggregate specifications.
func AggregateSpecsNameCollisions() func() {
	return func() {
		API("aggregate_specs_names", func() {})
		var Document = Type("Document", func() {
			Attribute("text", String, "Document text")
			Required("text")
		})
		Service("alpha", func() {
			Agent("scribe", "Writes documents", func() {
				Use("policy", func() {
					Tool("review", "Review a document", func() {
						Args(Document)
						Return(String)
					})
				})
				Use("tools", func() {
					Tool("write", "Write a document", func() {
						Args(Document)
						Return(String)
					})
				})
			})
		})
	}
}
