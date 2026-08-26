package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ToolsetNamedTools creates a scenario where the toolset is named "tools",
// which could conflict with the runtime tools package import.
func ToolsetNamedTools() func() {
	return func() {
		Service("alpha", func() {
			aidsl.Agent("helper", "Helper agent", func() {
				// Toolset named "tools" - this should not conflict with
				// goa.design/goa-ai/runtime/agent/tools import
				aidsl.Use("tools", func() {
					aidsl.Tool("do_something", "Does something", func() {
						aidsl.Args(func() {
							Attribute("input", String, "Input value")
							Required("input")
						})
						aidsl.Return(func() {
							Attribute("output", String, "Output value")
							Required("output")
						})
					})
				})
			})
		})
	}
}
