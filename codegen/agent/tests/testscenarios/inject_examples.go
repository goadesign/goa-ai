package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// InjectBoundMetaExample defines a bound tool that fills session_id from call
// metadata and household_id from labels fixed when the run starts. The
// generated provider must pass both values to the bound service method.
func InjectBoundMetaExample() func() {
	return func() {
		API("catalog", func() {})
		Service("catalog", func() {
			Method("get_data", func() {
				Payload(func() {
					Attribute("household_id", String, "Server-injected household identifier.", func() {
						MinLength(8)
					})
					Attribute("session_id", String, "Server-injected session identifier.", func() {
						MinLength(8)
					})
					Attribute("query", String, "Search query.")
					Required("household_id", "session_id", "query")
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
						Inject("household_id", "session_id")
					})
				})
			})
		})
	}
}

// InjectLocatedStringExample defines a bound tool whose session ID uses a
// String type generated in a shared package.
func InjectLocatedStringExample() func() {
	return func() {
		API("catalog", func() {})
		runtimeSessionID := Type("RuntimeSessionID", String, func() {
			MinLength(8)
			Meta("struct:pkg:path", "types")
		})
		lookupPayload := Type("LookupPayload", func() {
			Attribute("sessionId", runtimeSessionID, "Server-injected session identifier.")
			Required("sessionId")
		})
		Service("catalog", func() {
			Method("lookup", func() {
				Payload(lookupPayload)
				Result(String)
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						BindTo("lookup")
						Inject("sessionId")
					})
				})
			})
		})
	}
}

// InjectImportCollisionExample defines injected String types whose package
// names match packages used by the generated injection function.
func InjectImportCollisionExample() func() {
	return func() {
		API("catalog", func() {})
		runtimeSessionID := Type("RuntimeSessionID", String, func() {
			MinLength(8)
			Meta("struct:pkg:path", "utf8")
		})
		runID := Type("RunID", String, func() {
			Pattern("^run-")
			Meta("struct:pkg:path", "goa")
		})
		tenantID := Type("TenantID", String, func() {
			Pattern("^tenant-")
			Meta("struct:pkg:path", "fmt")
		})
		lookupPayload := Type("LookupPayload", func() {
			Attribute("session_id", runtimeSessionID, "Server-injected session identifier.")
			Attribute("run_id", runID, "Server-injected run identifier.")
			Attribute("tenant_id", tenantID, "Server-injected tenant identifier.", func() {
				Meta("struct:field:name", "OrganizationID")
			})
			Required("session_id", "run_id", "tenant_id")
		})
		Service("catalog", func() {
			Method("lookup", func() {
				Payload(lookupPayload)
				Result(String)
			})
			Agent("scribe", "Doc helper", func() {
				Use("helpers", func() {
					Tool("lookup", "Lookup", func() {
						BindTo("lookup")
						Inject("session_id", "run_id", "tenant_id")
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
		API("catalog", func() {})
		Service("catalog", func() {
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
