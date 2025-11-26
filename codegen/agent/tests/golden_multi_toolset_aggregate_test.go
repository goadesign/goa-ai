package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/testutil"
)

// Verifies aggregated specs import and merge multiple per-toolset packages.
func TestGolden_MultiToolset_Aggregate(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MultiToolset())
	content := fileContent(t, files, "gen/alpha/agents/scribe/specs/specs.go")
	testutil.AssertGo(t, "testdata/golden/multi_toolset/specs.go.golden", content)
}
