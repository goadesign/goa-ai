package testscenarios

import (
	. "goa.design/goa-ai/dsl"
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
			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(IDPayload)
						Return(OKResult)
						BindTo("alpha", "Find")
					})
				})
			})
		})
	}
}
