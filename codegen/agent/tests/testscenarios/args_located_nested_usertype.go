package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ArgsLocatedNestedUserType returns a DSL where the tool payload aliases a user
// type placed via struct:pkg:path (e.g. `types.*`) and that type references
// both another user type and a OneOf with a primitive branch.
//
// With newer Goa versions, types that are forced into a located package must
// ensure their dependencies are explicitly located as well. This scenario
// exercises codegen for a tool payload whose nested references live in a
// non-default package.
func ArgsLocatedNestedUserType() func() {
	return func() {
		API("alpha", func() {})

		var Status = Type("Status", String, func() {
			Description("Lifecycle status for a step.")
			Enum("pending", "in_progress", "completed", "blocked")
			Example("in_progress")
			Meta("struct:pkg:path", "types")
		})

		var ActiveStatus = Type("ActiveStatus", func() {
			Description("Details recorded while a step is active.")
			Attribute("status", Status, "Current lifecycle status.")
			Required("status")
			Meta("struct:pkg:path", "types")
		})

		var InactiveStatus = Type("InactiveStatus", func() {
			Description("A selected empty branch that records no additional value.")
			Meta("struct:pkg:path", "types")
		})

		var StatusChanged = Type("StatusChangedEvent", func() {
			Description("Status update event emitted during a run.")

			Attribute("step_id", String, "Step identifier.")
			Attribute("status", Status, "New step status.")
			Attribute("note", String, "Optional note about progress.")
			OneOf("state", "Selected step state.", func() {
				TypeName("StepState")
				Attribute("active", ActiveStatus, "The step is active.")
				Attribute("inactive", InactiveStatus, "The step is inactive.")
				Attribute("since", String, "Date when the current state began.", func() {
					Format(FormatDate)
				})
			})

			Required("step_id", "status", "state")

			Meta("struct:pkg:path", "types")
		})

		var SetStepStatusToolPayload = Type("SetStepStatusToolPayload", func() {
			Description("Tool input containing one shared status event.")
			Attribute("event", StatusChanged, "Status event to record.")
			Required("event")
		})

		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("progress", func() {
					Tool("set_step_status", "Set step status", func() {
						Args(SetStepStatusToolPayload)
						Return(Empty)
					})
				})
			})
		})
	}
}
