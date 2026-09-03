// Package testscenarios defines the exported no-result tool used by generated
// helper tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsNoResult declares one exported tool that accepts an argument and
// returns no value.
func ExportsNoResult() func() {
	return func() {
		API("alpha", func() {})
		var PurgeInput = Type("PurgeInput", func() {
			Attribute("before", String, "Remove documents that expired before this date")
			Required("before")
		})
		Service("alpha", func() {
			Agent("scribe", "Maintains documents", func() {
				Export("maintenance", func() {
					Tool("purge", "Remove expired documents", func() {
						Args(PurgeInput)
					})
				})
			})
		})
	}
}
