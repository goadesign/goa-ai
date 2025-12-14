package tests

import (
	"path/filepath"
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/testutil"
)

func TestGolden_Confirmation(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ConfirmationDSL())

	// Toolset-local tool specs must carry ConfirmationSpec.
	specs := fileContent(t, files, "gen/alpha/tools/atlas_commands/specs.go")
	assertGoldenGo(t, "confirmation", "specs.go.golden", specs)

	// Agent tool catalogue must surface confirmation metadata for UIs.
	cat := fileContent(t, files, "gen/alpha/agents/scribe/specs/tool_schemas.json")
	testutil.AssertString(t, filepath.Join("testdata", "golden", "confirmation", "tool_schemas.json.golden"), cat)
}
