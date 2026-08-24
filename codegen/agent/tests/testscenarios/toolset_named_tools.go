package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ToolsetNamedTools creates a scenario where the toolset is named "tools",
// which could conflict with the runtime tools package import.
func ToolsetNamedTools() func() {
	return func() {
		Service("alpha", func() {
			Agent("helper", "Helper agent", func() {
				// Toolset named "tools" - this should not conflict with
				// goa.design/goa-ai/runtime/agent/tools import
				Use("tools", func() {
					Tool("do_something", "Does something", func() {
						Args(func() {
							Attribute("input", String, "Input value")
							Required("input")
						})
						Return(func() {
							Attribute("output", String, "Output value")
							Required("output")
						})
					})
				})
			})
		})
	}
}
