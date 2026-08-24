package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelfServerDataPointerSidecar returns a DSL design function for a
// method-backed toolset that emits pointer-typed server_data from an optional
// bound method result field.
//
// The generated codecs must treat nil sidecar values as "no server_data" and
// therefore encode them to JSON null rather than returning an error.
func ServiceToolsetBindSelfServerDataPointerSidecar() func() {
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
						ServerData("aura.chart", Chart, func() {
							FromMethodResultField("chart")
						})
					})
				})
			})
		})
	}
}
