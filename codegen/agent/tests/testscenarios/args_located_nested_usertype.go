package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ArgsLocatedNestedUserType returns a DSL where the tool payload aliases a user
// type placed via struct:pkg:path (e.g. `types.*`) and that type references
// another user type.
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

		var StatusChanged = Type("StatusChangedEvent", func() {
			Description("Status update event emitted during a run.")

			Attribute("step_id", String, "Step identifier.")
			Attribute("status", Status, "New step status.")
			Attribute("note", String, "Optional note about progress.")

			Required("step_id", "status")

			Meta("struct:pkg:path", "types")
		})

		Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("progress", func() {
					Tool("set_step_status", "Set step status", func() {
						Args(StatusChanged)
						Return(Empty)
					})
				})
			})
		})
	}
}
