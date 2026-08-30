package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelfServerDataOptional returns a design with optional
// server data read from a service method result.
//
// The generated codec writes a nil value as JSON null instead of returning an
// error.
func ServiceToolsetBindSelfServerDataOptional() func() {
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
		var Chart = Type("Chart", func() {
			Attribute("title", String, "Chart title")
			Required("title")
		})

		Service("alpha", func() {
			Method("Find", func() {
				Payload(func() {
					Attribute("ident", String, "Identifier")
					Required("ident")
				})
				Result(func() {
					Attribute("okay", Boolean, "OK")
					Attribute("chart", Chart, "Optional chart sidecar")
					Required("okay")
				})
			})

			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(IDPayload)
						Return(OKResult)
						BindTo("alpha", "Find")
						ServerData("charts.preview", Chart, func() {
							FromMethodResultField("chart")
						})
					})
				})
			})
		})
	}
}
