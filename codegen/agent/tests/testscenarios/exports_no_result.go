// Package testscenarios defines the exported no-result tool used by generated
// helper tests.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportsNoResult declares one exported tool that accepts an argument and
// returns no value.
func ExportsNoResult() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Maintains documents", func() {
				aidsl.Export("maintenance", func() {
					aidsl.Tool("purge", "Remove expired documents", func() {
						aidsl.Args(String, "Remove documents that expired before this date.")
					})
				})
			})
		})
	}
}
