// Package testscenarios contains complete Goa designs used by agent generator tests.
package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportedRuntimeToolset declares an exported package named runtime and an
// agent that consumes it. The consumer also imports the goa-ai runtime package,
// so generation must choose two different import names.
func ExportedRuntimeToolset() func() {
	return func() {
		API("exported_runtime_toolset", func() {})
		Service("provider", func() {
			aidsl.Agent("source", "Provides shared tools.", func() {
				aidsl.Export("runtime", func() {
					aidsl.Tool("fetch", "Fetch data.", func() {
						aidsl.Args(String)
						aidsl.Return(String)
					})
				})
			})
		})
		Service("consumer", func() {
			aidsl.Agent("worker", "Uses shared tools.", func() {
				aidsl.Use(aidsl.AgentToolset("provider", "source", "runtime"))
			})
		})
	}
}
