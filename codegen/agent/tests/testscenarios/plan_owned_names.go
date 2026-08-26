// Package testscenarios defines focused Goa and Goa-AI designs used by agent
// generator tests.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// PlanOwnedNames defines colliding tool and import names plus service fields
// whose final Go selectors differ from their DSL names.
func PlanOwnedNames() func() {
	return func() {
		API("planned", func() {})
		var LookupPayload = Type("LookupPayload", func() {
			Attribute("session_id", String, "Session identifier", func() {
				Meta("struct:field:name", "BoundSession")
			})
			Attribute("query", String, "Search query")
			Attribute("cursor", String, "Cursor for the requested page")
			Required("session_id", "query")
		})
		var LookupResult = Type("LookupResult", func() {
			Attribute("items", ArrayOf(String), "Matching items")
			Required("items")
		})
		var Evidence = Type("Evidence", func() {
			Attribute("kind", String, "Evidence kind")
			Required("kind")
		})
		Service("runtime", func() {
			Method("Find", func() {
				Payload(func() {
					Attribute("session_id", String, "Session identifier")
					Attribute("query", String, "Search query")
					Attribute("cursor", String, "Cursor for the requested page")
					Required("session_id", "query")
				})
				Result(func() {
					Attribute("items", ArrayOf(String), "Matching items")
					Attribute("returned", Int, "Returned count", func() {
						Meta("struct:field:name", "ReturnedCount")
					})
					Attribute("truncated", Boolean, "Whether more items exist", func() {
						Meta("struct:field:name", "WasTruncated")
					})
					Attribute("next_cursor", String, "Cursor for the next page", func() {
						Meta("struct:field:name", "FollowingCursor")
					})
					Attribute("evidence", ArrayOf(Evidence), "Evidence kept from the model", func() {
						Meta("struct:field:name", "AttachedEvidence")
					})
					Required("items", "returned", "truncated", "evidence")
				})
			})
			aidsl.Agent("scribe", "Search helper", func() {
				aidsl.Use("lookup", func() {
					for _, name := range []string{"lookup1", "lookup_1"} {
						aidsl.Tool(name, "Look up items", func() {
							aidsl.Args(LookupPayload)
							aidsl.Return(LookupResult)
							aidsl.Inject("session_id")
							aidsl.BindTo("runtime", "Find")
							aidsl.BoundedResult(func() {
								aidsl.Cursor("cursor")
								aidsl.NextCursor("next_cursor")
							})
							aidsl.ServerData("aura.evidence", ArrayOf(Evidence), func() {
								aidsl.FromMethodResultField("evidence")
							})
						})
					}
				})
			})
		})
	}
}
