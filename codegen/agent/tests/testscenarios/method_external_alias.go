package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MethodExternalAlias defines a method-bound tool whose result aliases a
// specs-local type while nesting a user type with a custom package path.
// It exercises transform initialization to ensure the top-level initializer
// uses the specs package while nested fields preserve external packages.
func MethodExternalAlias() func() {
	return func() {
		API("alpha", func() {})

		// External user type located under a custom package path.
		var Ext = Type("Ext", func() {
			Description("External type")
			Meta("struct:pkg:path", "example.com/mod/gen/types")
			Attribute("id", String, "ID")
			Required("id")
		})

		// Service result using the external type and a collection
		var SvcRes = Type("SvcRes", func() {
			Attribute("ext", Ext, "External value")
			Attribute("items", ArrayOf(Ext), "External list")
			Required("ext", "items")
		})

		// Tool result (specs-local alias) with the same shape
		var ToolRes = Type("ToolRes", func() {
			Attribute("ext", Ext, "External value")
			Attribute("items", ArrayOf(Ext), "External list")
			Required("ext", "items")
		})

		Service("svc", func() {
			Method("Fetch", func() {
				Payload(func() {
					Attribute("session_id", String)
					Required("session_id")
				})
				Result(SvcRes)
			})
		})

		Service("alpha", func() {
			Agent("scribe", "Test agent", func() {
				Uses(func() {
					Toolset("svcset", func() {
						Tool("fetch", "Fetch data", func() {
							Return(ToolRes)
							BindTo("svc", "Fetch")
						})
					})
				})
			})
		})
	}
}
