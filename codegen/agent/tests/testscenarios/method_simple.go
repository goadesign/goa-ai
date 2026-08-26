package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MethodSimpleCompatible defines a simple method-bound tool whose shapes are compatible
// to trigger transform emission (Args -> Method Payload, Method Result -> Return).
func MethodSimpleCompatible() func() {
	return func() {
		API("svc", func() {})
		var QPayload = Type("QPayload", func() {
			Attribute("q", String, "Q")
			Required("q")
		})
		var OkResult = Type("OkResult", func() {
			Attribute("ok", Boolean, "OK")
		})
		Service("svc", func() {
			Method("Do", func() {
				Payload(func() {
					Attribute("q", String, "Q")
					Required("q")
				})
				Result(func() {
					Attribute("ok", Boolean, "OK")
				})
			})
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("lookup", func() {
					aidsl.Tool("by_id", "Lookup by ID", func() {
						aidsl.Args(QPayload)
						aidsl.Return(OkResult)
						aidsl.BindTo("Do")
					})
				})
			})
		})
	}
}
