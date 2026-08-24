package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// InjectBoundMetaExample defines a bound tool that fills session_id from call
// metadata and household_id from labels fixed when the run starts. The
// generated provider must pass both values to the bound service method.
func InjectBoundMetaExample() func() {
	return func() {
		API("atlas", func() {})
		Service("atlas", func() {
			Method("get_data", func() {
				Payload(func() {
					Attribute("household_id", String, "Server-injected household identifier.")
					Attribute("session_id", String, "Server-injected session identifier.")
					Attribute("query", String, "Search query.")
					Required("household_id", "session_id", "query")
				})
				Result(func() {
					Attribute("ok", Boolean, "Whether the lookup succeeded.")
					Required("ok")
				})
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("get_data", "Get data", func() {
						aidsl.BindTo("get_data")
						aidsl.Inject("household_id", "session_id")
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
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("lookup_household", "Lookup scoped to a household", func() {
						aidsl.Args(LookupByHousehold)
						aidsl.Return(LookupResult)
						aidsl.Inject("household_id", "session_id")
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
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("lookup_household", "Lookup scoped to a household", func() {
						aidsl.Args(func() {
							Attribute("household_id", String, "Household to scope the search to.")
							Attribute("query", String, "Search query.")
							Required("household_id", "query")
						})
						aidsl.Return(func() {
							Attribute("ok", Boolean, "Whether the lookup succeeded.")
							Required("ok")
						})
						aidsl.Inject("household_id")
					})
				})
				aidsl.Use("audit", func() {
					aidsl.Tool("record_access", "Record a data access", func() {
						aidsl.Args(func() {
							Attribute("tenant_id", String, "Tenant that owns the audit trail.")
							Attribute("household_id", String, "Household the access touched.")
							Attribute("action", String, "Action performed.")
							Required("tenant_id", "household_id", "action")
						})
						aidsl.Return(func() {
							Attribute("ok", Boolean, "Whether the record was written.")
							Required("ok")
						})
						aidsl.Inject("tenant_id", "household_id")
					})
				})
			})
		})
	}
}

// InjectMixedBoundUnboundExample defines a single toolset mixing a
// method-backed (BindTo) tool that declares NO aidsl.Inject() fields with an
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
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("get_data", "Get data", func() {
						aidsl.BindTo("get_data")
					})
					aidsl.Tool("lookup_household", "Lookup scoped to a household", func() {
						aidsl.Args(func() {
							Attribute("household_id", String, "Household to scope the search to.", func() {
								Pattern("^[a-z0-9-]+$")
							})
							Attribute("query", String, "Search query.")
							Required("household_id", "query")
						})
						aidsl.Return(func() {
							Attribute("ok", Boolean, "Whether the lookup succeeded.")
							Required("ok")
						})
						aidsl.Inject("household_id")
					})
				})
			})
		})
	}
}

