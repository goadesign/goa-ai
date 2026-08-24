package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ArgsUnionSumTypes returns a DSL with union (OneOf) args and result.
func ArgsUnionSumTypes() func() {
	return func() {
		API("alpha", func() {})

		var StructuredValue = Type("StructuredValue", func() {
			Attribute("label", String, "Structured value label")
			Required("label")
		})

		var UnionPayload = Type("UnionPayload", func() {
			Attribute("id", String, "Request identifier")
			OneOf("value", func() {
				Attribute("number", Int32, "Numeric value")
				Attribute("text", String, "Text value")
				Attribute("structured", StructuredValue, "Structured value")
			})
			OneOf("optional_value", func() {
				Attribute("number", Int32, "Optional numeric value")
				Attribute("text", String, "Optional text value")
			})
			Required("id", "value")
		})

		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("union", func() {
					aidsl.Tool("echo", "Echo union", func() {
						aidsl.Args(UnionPayload)
						aidsl.Return(UnionPayload)
					})
				})
			})
		})
	}
}
