// Package testscenarios contains complete Goa designs used by agent generator tests.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// ExportedRuntimeToolset declares an exported package named runtime and an
// agent that consumes it. The consumer also imports the goa-ai runtime package,
// so generation must choose two different import names.
func ExportedRuntimeToolset() func() {
	return func() {
		API("exported_runtime_toolset", func() {})
		Service("provider", func() {
			Agent("source", "Provides shared tools.", func() {
				Export("runtime", func() {
					Tool("fetch", "Fetch data.", func() {
						Args(String)
						Return(String)
					})
				})
			})
		})
		Service("consumer", func() {
			Agent("worker", "Uses shared tools.", func() {
				Use(AgentToolset("provider", "source", "runtime"))
			})
		})
	}
}
