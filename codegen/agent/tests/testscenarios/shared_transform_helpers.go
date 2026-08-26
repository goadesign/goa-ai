package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// SharedTransformHelpers defines two required nested values and one optional
// nested value across separate service-backed tools.
func SharedTransformHelpers() func() {
	return func() {
		API("alpha", func() {})

		var Child = Type("SharedChild", func() {
			Attribute("value", String, "Value")
			Required("value")
		})
		var RequiredPayload = Type("RequiredPayload", func() {
			Attribute("child", Child, "Child value")
			Required("child")
		})
		var OptionalPayload = Type("OptionalPayload", func() {
			Attribute("child", Child, "Child value")
		})

		Service("alpha", func() {
			Method("First", func() {
				Payload(RequiredPayload)
				Result(RequiredPayload)
			})
			Method("Second", func() {
				Payload(RequiredPayload)
				Result(RequiredPayload)
			})
			Method("Optional", func() {
				Payload(OptionalPayload)
				Result(OptionalPayload)
			})
			aidsl.Agent("worker", "Worker", func() {
				aidsl.Use("helpers", func() {
					aidsl.Tool("first", "Use the first required value.", func() {
						aidsl.BindTo("First")
					})
					aidsl.Tool("second", "Use the second required value.", func() {
						aidsl.BindTo("Second")
					})
					aidsl.Tool("optional", "Use the optional value.", func() {
						aidsl.BindTo("Optional")
					})
				})
			})
		})
	}
}
