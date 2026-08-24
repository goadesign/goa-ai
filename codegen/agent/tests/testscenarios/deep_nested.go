package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// DeepNestedValidations defines nested user types with validations at each level.
func DeepNestedValidations() func() {
	return func() {
		API("alpha", func() {})

		var Level3 = Type("Level3", func() {
			Description("Level 3 leaf")
			Attribute("leaf", String, "Leaf value")
			Required("leaf")
		})

		var Level2 = Type("Level2", func() {
			Description("Level 2 node")
			Attribute("mid", String, "Middle value")
			Attribute("child", Level3, "Child L3")
			Required("mid", "child")
		})

		var Level1 = Type("Level1", func() {
			Description("Level 1 root")
			Attribute("root", String, "Root value")
			Attribute("child", Level2, "Child L2")
			Attribute("labels", MapOf(String, String), "Open labels keyed by source")
			Attribute("objects", MapOf(String, Level3), "Open objects keyed by source")
			Attribute("groups", MapOf(String, MapOf(String, String)), "Nested labels keyed by group and source")
			Attribute("counts", MapOf(String, Int), "Integer counts keyed by source")
			Required("root", "child")
		})

		Service("alpha", func() {
			aidsl.Agent("scribe", "Deep nested validator test", func() {
				aidsl.Use("deep", func() {
					aidsl.Tool("validate", "Validate nested payload", func() {
						aidsl.Args(Level1)
						aidsl.Return(Level1)
					})
				})
			})
		})
	}
}
