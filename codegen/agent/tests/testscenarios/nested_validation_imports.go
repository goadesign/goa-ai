package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// NestedValidationImports defines a tool whose nested transport validator
// needs a package that the top-level validator does not use.
func NestedValidationImports() func() {
	return func() {
		API("alpha", func() {})

		nested := Type("NestedText", func() {
			Attribute("value", String, "Text supplied inside the nested object", func() {
				MinLength(2)
			})
			Required("value")
		})
		payload := Type("NestedPayload", func() {
			Attribute("nested", nested, "Nested text to validate")
			Required("nested")
		})

		Service("alpha", func() {
			aidsl.Agent("scribe", "Validates nested text", func() {
				aidsl.Use("nested", func() {
					aidsl.Tool("validate", "Validate nested text", func() {
						aidsl.Args(payload)
						aidsl.Return(String, "Validated text")
					})
				})
			})
		})
	}
}
