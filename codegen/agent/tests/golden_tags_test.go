package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Tags should be surfaced in tool specs.
func TestGolden_Tags(t *testing.T) {
	design := testscenarios.TagsBasic()
	files := buildAndGenerate(t, design)
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/specs.go")
	assertGoldenGo(t, "tags", "specs.go.golden", specs)
}
