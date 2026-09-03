// Package tests verifies that an agent consuming another agent's tools uses the
// exact Go names declared in the exporting agent's generated package.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestGoldenExportedToolsetConsumer checks the generated aggregate used by an
// agent that consumes another agent's exported toolset.
func TestGoldenExportedToolsetConsumer(t *testing.T) {
	files := buildAndGenerate(t, exportedToolsetConsumerDesign())
	aggregate := fileContent(t, files, "gen/consumer/agents/worker/specs/specs.go")

	require.Contains(t, aggregate, "ada.SpecFetch()")
	require.Contains(t, aggregate, `spec.AgentID = "provider.source"`)
	require.Contains(t, aggregate, "ada.Fetch")
	require.NotContains(t, aggregate, "ada.,")
	assertGoldenGo(t, "exported_toolset_consumer", "specs.go.golden", aggregate)
}

// exportedToolsetConsumerDesign declares one provider and one consumer so the
// generated consumer must read names selected in the provider's tool package.
func exportedToolsetConsumerDesign() func() {
	return func() {
		API("exported_toolset_consumer", func() {})
		var FetchInput = Type("FetchInput", func() {
			Attribute("key", String, "Data key")
			Required("key")
		})
		Service("provider", func() {
			Agent("source", "Provides shared tools.", func() {
				Export("ada", func() {
					Tool("fetch", "Fetch data.", func() {
						Args(FetchInput)
						Return(String)
					})
				})
			})
		})
		Service("consumer", func() {
			Agent("worker", "Uses shared tools.", func() {
				Use(AgentToolset("provider", "source", "ada"))
			})
		})
	}
}
