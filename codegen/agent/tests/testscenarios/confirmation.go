package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ConfirmationDSL returns a DSL design function for confirmation tool specs.
func ConfirmationDSL() func() {
	return func() {
		API("alpha", func() {})

		var DangerousWriteArgs = Type("DangerousWriteArgs", func() {
			Attribute("key", String, "Key to update.")
			Attribute("value", String, "New value.")
			Required("key", "value")
		})

		var DangerousWriteResult = Type("DangerousWriteResult", func() {
			Attribute("summary", String, "Summary of the update.")
			Attribute("key", String, "Key that was updated.")
			Required("summary", "key")
		})

		var Commands = Toolset("atlas.commands", func() {
			Description("Write operations that require explicit operator confirmation.")

			Tool("dangerous_write", "Write a stateful change", func() {
				Args(DangerousWriteArgs)
				Return(DangerousWriteResult)
				Confirmation(func() {
					Title("Confirm change")
					PromptTemplate(`Approve write: set {{ .Key }} to {{ .Value }}`)
					DeniedResultTemplate(`{"summary":"Cancelled","key":"{{ .Key }}"}`)
				})
			})
		})

		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use(Commands)
			})
		})
	}
}
