package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceToolsetBindSelfHints returns a DSL design function for a method-backed
// used toolset that includes call and result hint templates.
func ServiceToolsetBindSelfHints() func() {
	return func() {
		API("alpha", func() {})
		var idPayload = Type("HintIDPayload", func() {
			Attribute("id", String, "ID")
			Required("id")
		})
		var okResult = Type("HintOKResult", func() {
			Attribute("ok", Boolean, "OK")
			Required("ok")
		})
		Service("alpha", func() {
			Method("Find", func() {
				Payload(func() {
					Attribute("ident", String, "Identifier")
					Required("ident")
				})
				Result(func() {
					Attribute("okay", Boolean, "OK")
					Required("okay")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(idPayload)
						Return(okResult)
						BindTo("alpha", "Find")
						CallHintTemplate("Lookup {{ .ID }}")
						ResultHintTemplate("Done {{ .Result.Ok }}")
					})
				})
			})
		})
	}
}
