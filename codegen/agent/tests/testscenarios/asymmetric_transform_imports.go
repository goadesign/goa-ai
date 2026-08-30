// Package testscenarios defines small Goa designs used by generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// AsymmetricTransformImports defines matching tool and service values whose
// nested generated types belong to different packages.
func AsymmetricTransformImports() func() {
	return func() {
		API("alpha", func() {})

		serviceGrandchild := Type("ServiceGrandchild", func() {
			Attribute("label", String, "Label")
			Required("label")
			Meta("struct:pkg:path", "service/grandchild")
		})
		serviceChild := Type("ServiceChild", func() {
			Attribute("grandchild", serviceGrandchild, "Nested service value")
			Required("grandchild")
		})
		serviceValue := Type("ServiceValue", func() {
			Attribute("child", serviceChild, "Service value")
			Required("child")
		})
		toolGrandchild := Type("ToolGrandchild", func() {
			Attribute("label", String, "Label")
			Required("label")
			Meta("struct:pkg:path", "tool/grandchild")
		})
		toolChild := Type("ToolChild", func() {
			Attribute("grandchild", toolGrandchild, "Nested tool value")
			Required("grandchild")
		})
		toolValue := Type("ToolValue", func() {
			Attribute("child", toolChild, "Tool value")
			Required("child")
		})

		Service("alpha", func() {
			Method("Convert", func() {
				Payload(serviceValue)
				Result(serviceValue)
			})
			Agent("worker", "Worker", func() {
				Use("values", func() {
					Tool("convert", "Converts one nested value.", func() {
						Args(toolValue)
						Return(toolValue)
						BindTo("Convert")
					})
				})
			})
		})
	}
}
