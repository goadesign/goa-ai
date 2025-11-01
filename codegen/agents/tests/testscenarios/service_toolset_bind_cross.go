package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindCross returns a DSL with an agent binding to another service method.
func ServiceToolsetBindCross() func() {
	return func() {
		API("multi", func() {})
		var IDPayload = Type("IDPayload", func() { Attribute("id", String, "ID"); Required("id") })
		var OKResult = Type("OKResult", func() { Attribute("ok", Boolean, "OK"); Required("ok") })
		Service("bravo", func() {
			Method("Lookup", func() {
				Payload(func() { Attribute("id", String, "Identifier"); Required("id") })
				Result(func() { Attribute("ok", Boolean, "OK"); Required("ok") })
			})
		})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("lookup", func() {
						Tool("by_id", "Lookup by ID", func() {
							Args(IDPayload)
							Return(OKResult)
							BindTo("bravo", "Lookup")
						})
					})
				})
			})
		})
	}
}
