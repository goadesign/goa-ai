package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ServiceCompletionPrimitive returns a DSL with a completion that produces a
// primitive result so codec generation cannot assume transport helper types.
func ServiceCompletionPrimitive() func() {
	return func() {
		API("tasks", func() {})

		Service("tasks", func() {
			Completion("headline", "Write a short task headline", func() {
				Return(String)
			})
		})
	}
}
