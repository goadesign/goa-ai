package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ArgsUnionSumTypes returns a DSL with union (OneOf) args and result.
func ArgsUnionSumTypes() func() {
	return func() {
		API("alpha", func() {})

		var UnionPayload = Type("UnionPayload", func() {
			Attribute("id", String, "Request identifier")
			OneOf("value", func() {
				Attribute("number", Int32, "Numeric value")
				Attribute("text", String, "Text value")
			})
			Required("id", "value")
		})

		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("union", func() {
					Tool("echo", "Echo union", func() {
						Args(UnionPayload)
						Return(UnionPayload)
					})
				})
			})
		})
	}
}
