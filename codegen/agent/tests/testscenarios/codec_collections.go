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
			Attribute("unsigned_numbers", ArrayOf(UInt32), "Unsigned integers.")
			Attribute("large_numbers", ArrayOfRequired(Int64), "Required large integers.")
			Attribute("counts", MapOf(String, Int32), "Integer counts by name.")
			Attribute("groups", ArrayOf(MapOf(String, Int32)), "Integer counts grouped by position.")
			Attribute("node", "Node", "Recursive node.")
			Attribute("dynamic", Any, "Unrestricted JSON value.")
			Required("aliases", "numbers", "large_numbers")
		})
		archivePayload := Type("ArchivePayload", func() {
			Attribute("aliases", ArrayOfRequired(alias), "Archived aliases.")
			Attribute("numbers", ArrayOfRequired(Int32), "Archived integers.")
			Attribute("unsigned_numbers", ArrayOf(UInt32), "Archived unsigned integers.")
			Attribute("large_numbers", ArrayOfRequired(Int64), "Archived large integers.")
			Attribute("counts", MapOf(String, Int32), "Archived counts by name.")
			Attribute("groups", ArrayOf(MapOf(String, Int32)), "Archived counts grouped by position.")
			Attribute("node", "Node", "Archived recursive node.")
			Attribute("dynamic", Any, "Archived unrestricted JSON value.")
			Required("aliases", "numbers", "large_numbers")
		})

		Service("alpha", func() {
			aidsl.Agent("scribe", "Collection codec test", func() {
				aidsl.Use("collections", func() {
					aidsl.Tool("store", "Store typed collections", func() {
						aidsl.Args(payload)
						aidsl.Return(payload)
					})
					aidsl.Tool("archive", "Archive typed collections", func() {
						aidsl.Args(archivePayload)
					})
				})
			})
		})
	}
}
