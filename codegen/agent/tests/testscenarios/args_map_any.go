// Package testscenarios contains Goa designs used by generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ArgsMapAny returns a tool whose open map accepts arbitrary JSON values.
func ArgsMapAny() func() {
	return func() {
		API("alpha", func() {})

		var inspectPayload = Type("InspectPayload", func() {
			Attribute("metadata", MapOf(String, Any), "Caller-defined metadata values.")
		})

		Service("alpha", func() {
			Agent("scribe", "Metadata helper", func() {
				Use("records", func() {
					Tool("inspect", "Inspect metadata", func() {
						Args(inspectPayload)
					})
				})
			})
		})
	}
}
