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

// InjectMultiToolsetLabelsExample defines an agent using TWO toolsets whose
// tools inject overlapping label-backed fields (helpers: household_id;
// audit: household_id + tenant_id). It exercises the agent-level
// RequiredLabels aggregation: the generated specs aggregate package must
// expose the sorted, deduplicated union of every used toolset's
// RequiredLabels, and the generated registry.go must wire that var onto
// AgentRegistration for run-start enforcement.
func InjectMultiToolsetLabelsExample() func() {
	return func() {
		API("calc", func() {})
		Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup_household", "Lookup scoped to a household", func() {
						Args(func() {
							Attribute("household_id", String, "Household to scope the search to.")
							Attribute("query", String, "Search query.")
							Required("household_id", "query")
						})
						Return(func() {
							Attribute("ok", Boolean, "Whether the lookup succeeded.")
							Required("ok")
						})
						Inject("household_id")
					})
				})
				Use("audit", func() {
					Tool("record_access", "Record a data access", func() {
						Args(func() {
							Attribute("tenant_id", String, "Tenant that owns the audit trail.")
							Attribute("household_id", String, "Household the access touched.")
							Attribute("action", String, "Action performed.")
							Required("tenant_id", "household_id", "action")
						})
						Return(func() {
							Attribute("ok", Boolean, "Whether the record was written.")
							Required("ok")
						})
						Inject("tenant_id", "household_id")
					})
				})
			})
		})
	}
}

// InjectMixedBoundUnboundExample defines a single toolset mixing a
// method-backed (BindTo) tool that declares NO Inject() fields with an
// unbound tool that injects a label-backed field. The generated registry
// provider.go only emits dispatch cases for method-backed tools, so its
// runtime.ToolCallMeta construction must be gated on injecting
// METHOD-BACKED tools -- gating on "any tool injects" emits a
// declared-and-unused meta variable and the generated package fails to
// compile (the exact regression this scenario locks).
func InjectMixedBoundUnboundExample() func() {
	return func() {
		API("atlas", func() {})
		Service("atlas", func() {
			Method("get_data", func() {
				Payload(func() {
					Attribute("query", String, "Search query.")
					Required("query")
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
					})
					Tool("lookup_household", "Lookup scoped to a household", func() {
						Args(func() {
							Attribute("household_id", String, "Household to scope the search to.", func() {
								Pattern("^[a-z0-9-]+$")
							})
							Attribute("query", String, "Search query.")
							Required("household_id", "query")
						})
						Return(func() {
							Attribute("ok", Boolean, "Whether the lookup succeeded.")
							Required("ok")
						})
						Inject("household_id")
					})
				})
			})
		})
	}
}
