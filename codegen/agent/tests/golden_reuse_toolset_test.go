package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Reusing a top-level toolset in Uses should not duplicate specs.
func TestGolden_ReUse(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ReUse())
	// With per-toolset specs, ensure aggregation works; check aggregated specs package.
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/specs.go")
	assertGoldenGo(t, "reuse_toolset", "specs.go.golden", specs)
}
