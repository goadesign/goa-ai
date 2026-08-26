// Package testscenarios defines small Goa designs used by generator tests.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// CodecCollections defines required primitive-alias arrays, nested integer
// collections, an open value, and a recursive object for generated codec tests.
func CodecCollections() func() {
	return func() {
		API("alpha", func() {})

		alias := Type("Alias", String, func() {
			Pattern("^[a-z]+$")
		})
		_ = Type("Node", func() {
			Attribute("name", String, "Node name.")
			Attribute("next", "Node", "Next node.")
			Required("name")
		})
		payload := Type("CollectionPayload", func() {
			Attribute("aliases", ArrayOfRequired(alias), "Required aliases.")
			Attribute("numbers", ArrayOfRequired(Int32), "Required integers.")
			Attribute("counts", MapOf(String, Int32), "Integer counts by name.")
			Attribute("groups", ArrayOf(MapOf(String, Int32)), "Integer counts grouped by position.")
			Attribute("node", "Node", "Recursive node.")
			Attribute("dynamic", Any, "Unrestricted JSON value.")
			Required("aliases", "numbers")
		})

		Service("alpha", func() {
			aidsl.Agent("scribe", "Collection codec test", func() {
				aidsl.Use("collections", func() {
					aidsl.Tool("store", "Store typed collections", func() {
						aidsl.Args(payload)
						aidsl.Return(payload)
					})
				})
			})
		})
	}
}
