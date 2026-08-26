package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
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
			aidsl.Agent("scribe", "Doc helper", func() {
				aidsl.Use("ops", func() {
					aidsl.Tool("echo", "Echo", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
				})
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
