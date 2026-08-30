// Package testscenarios defines a service export that reuses one shared result
// containing a OneOf field.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

// LocatedSharedOneOfServiceExport returns a design where a service result and
// its exported tool both refer to one type generated in gen/types.
func LocatedSharedOneOfServiceExport() func() {
	return func() {
		API("located shared oneof service export", func() {})

		observed := locatedSharedType("ObservedCycles", func() {
			Attribute("count", Int, "Number of completed cycles.")
			Required("count")
		})
		none := locatedSharedType("NoCycles", func() {})
		facts := locatedSharedType("CycleFacts", func() {
			OneOf("conclusion", func() {
				Attribute("observed", observed, "One or more cycles completed.")
				Attribute("none", none, "No cycle completed.")
			})
			Required("conclusion")
		})
		toolResult := Type("CycleToolResult", func() {
			Attribute("results", ArrayOf(facts), "Cycle facts in source order.")
			Required("results")
		})
		serviceResult := Type("CycleServiceResult", func() {
			Extend(toolResult)
			Attribute("returned", Int, "Number of returned source results.")
			Required("returned")
		})
		read := Toolset("read", func() {
			Tool("cycles", "Returns completed cycle facts.", func() {
				Return(toolResult)
				BindTo("records", "Cycles")
			})
		})

		Service("records", func() {
			Method("Cycles", func() {
				Result(serviceResult)
			})
			Export(read)
		})
	}
}

// locatedSharedType creates one type that Goa writes to gen/types even when
// more than one generated package refers to it.
func locatedSharedType(name string, define func()) goaexpr.UserType {
	return Type(name, func() {
		define()
		Meta("struct:pkg:path", "types")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
	})
}
