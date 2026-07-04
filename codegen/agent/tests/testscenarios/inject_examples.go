package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// InjectBoundMetaExample defines a BindTo tool that injects a meta-backed
// field (session_id) from the bound service method's payload. It exercises
// the historical population path (the generated registry provider.go, which
// this task leaves unchanged) alongside the new generated inject.go, proving
// both resolve session_id identically.
func InjectBoundMetaExample() func() {
	return func() {
		API("atlas", func() {})
		Service("atlas", func() {
			Method("get_data", func() {
				Payload(func() {
					Attribute("session_id", String, "Server-injected session identifier.")
					Attribute("query", String, "Search query.")
					Required("session_id", "query")
				})
				Result(func() {
					Attribute("ok", Boolean, "Whether the lookup succeeded.")
					Required("ok")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("get_data", "Get data", func() {
						BindTo("get_data")
						Inject("session_id")
					})
				})
			})
		})
	}
}

// InjectLabelExample defines an unbound tool that injects both a meta-backed
// field (session_id) and a label-backed field (household_id) with a pattern
// validation, exercising mixed compiled injection sources on a single tool.
func InjectLabelExample() func() {
	return func() {
		API("calc", func() {})
		var LookupByHousehold = Type("LookupByHousehold", func() {
			Attribute("household_id", String, "Household to scope the search to.", func() {
				Pattern("^[a-z0-9-]+$")
			})
			Attribute("session_id", String, "Server-injected session identifier.")
			Attribute("query", String, "Search query.")
			Required("household_id", "session_id", "query")
		})
		var LookupResult = Type("LookupResult", func() {
			Attribute("ok", Boolean, "Whether the lookup succeeded.")
			Required("ok")
		})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup_household", "Lookup scoped to a household", func() {
						Args(LookupByHousehold)
						Return(LookupResult)
						Inject("household_id", "session_id")
					})
				})
			})
		})
	}
}
