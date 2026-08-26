// Package testscenarios contains Goa designs used by full agent generator tests.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AggregateSpecsNameCollisions declares toolsets whose package names match the
// runtime packages imported by the agent's aggregate specifications.
func AggregateSpecsNameCollisions() func() {
	return func() {
		API("aggregate_specs_names", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Writes documents", func() {
				aidsl.Use("policy", func() {
					aidsl.Tool("review", "Review a document", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
				})
				aidsl.Use("tools", func() {
					aidsl.Tool("write", "Write a document", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
				})
			})
		})
	}
}
