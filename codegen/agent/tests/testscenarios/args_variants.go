package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ArgsPrimitive returns a DSL with primitive args and result.
func ArgsPrimitive() func() {
	return func() {
		API("alpha", func() {})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("ops", func() {
					aidsl.Tool("echo", "Echo", func() {
						aidsl.Args(String, "text to echo")
						aidsl.Return(String, "echoed text")
					})
				})
			})
		})
	}
}

// ArgsInlineObject returns a DSL with inline object args and result.
func ArgsInlineObject() func() {
	return func() {
		API("alpha", func() {})
		var AddPayload = Type("AddPayload", func() {
			Attribute("left", Int32, "Left operand")
			Attribute("right", Int32, "Right operand")
			Required("left", "right")
		})
		var AddResult = Type("AddResult", func() {
			Attribute("sum", Int32, "Sum")
			Required("sum")
		})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("math", func() {
					aidsl.Tool("add", "Add", func() {
						aidsl.Args(AddPayload)
						aidsl.Return(AddResult)
					})
				})
			})
		})
	}
}

// ArgsUserType returns a DSL with user type args and result (service-local type).
func ArgsUserType() func() {
	return func() {
		API("alpha", func() {})
		var Doc = Type("Doc", func() {
			Attribute("id", String, "Identifier")
			Attribute("title", String, "Title")
			Required("id")
		})
		Service("alpha", func() {
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("docs", func() {
					aidsl.Tool("store", "Store", func() {
						aidsl.Args(Doc, func() { Required("title") })
						aidsl.Return(Doc)
					})
				})
			})
		})
	}
}
