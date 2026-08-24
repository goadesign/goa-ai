package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelf returns a DSL design function for a method-backed toolset within the same service.
func ServiceToolsetBindSelf() func() {
	return func() {
		API("alpha", func() {})
		var IDPayload = Type("IDPayload", func() { Attribute("id", String, "ID"); Required("id") })
		var OKResult = Type("OKResult", func() { Attribute("ok", Boolean, "OK"); Required("ok") })
		Service("alpha", func() {
			Method("Find", func() {
				Payload(func() { Attribute("ident", String, "Identifier"); Required("ident") })
				Result(func() { Attribute("okay", Boolean, "OK"); Required("okay") })
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("lookup", func() {
					aidsl.Tool("by_id", "Lookup by ID", func() {
						aidsl.Args(IDPayload)
						aidsl.Return(OKResult)
						aidsl.BindTo("alpha", "Find")
					})
				})
			})
		})
	}
}
