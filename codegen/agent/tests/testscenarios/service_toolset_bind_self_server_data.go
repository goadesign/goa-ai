package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelfServerData returns a DSL design function for a
// method-backed toolset that emits server_data from a bound method result field.
func ServiceToolsetBindSelfServerData() func() {
	return func() {
		API("alpha", func() {})
		var IDPayload = Type("IDPayload", func() {
			Attribute("id", String, "ID")
			Required("id")
		})
		var OKResult = Type("OKResult", func() {
			Attribute("ok", Boolean, "OK")
			Required("ok")
		})
		var Evidence = Type("Evidence", func() {
			Attribute("kind", String, "Evidence kind")
			Required("kind")
		})
		Service("alpha", func() {
			Method("Find", func() {
				Payload(func() {
					Attribute("ident", String, "Identifier")
					Required("ident")
				})
				Result(func() {
					Attribute("okay", Boolean, "OK")
					Attribute("evidence", ArrayOf(Evidence), "Evidence emitted by the method")
					Required("okay", "evidence")
				})
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("lookup", func() {
					aidsl.Tool("by_id", "Lookup by ID", func() {
						aidsl.Args(IDPayload)
						aidsl.Return(OKResult)
						aidsl.BindTo("alpha", "Find")
						aidsl.ServerData("aura.evidence", ArrayOf(Evidence), func() {
							aidsl.FromMethodResultField("evidence")
						})
					})
				})
			})
		})
	}
}
