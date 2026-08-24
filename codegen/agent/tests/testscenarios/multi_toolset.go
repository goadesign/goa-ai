package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MultiToolset returns a DSL design with two toolsets under one agent to
// exercise the aggregated specs package importing multiple per-toolset packages.
func MultiToolset() func() {
	return func() {
		API("alpha", func() {})
		var AddPayload = Type("AddPayload", func() {
			Attribute("left", Int32, "Left operand")
			Attribute("right", Int32, "Right operand")
		})
		var AddResult = Type("AddResult", func() {
			Attribute("sum", Int32, "Sum")
		})
		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("ops", func() {
					Tool("echo", "Echo", func() {
						Args(String)
						Return(String)
					})
				})
				Use("math", func() {
					Tool("add", "Add", func() {
						Args(AddPayload)
						Return(AddResult)
					})
				})
			})
		})
	}
}
