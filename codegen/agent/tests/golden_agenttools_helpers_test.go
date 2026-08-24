package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verify that exported toolsets receive typed New<Tool>Call helpers that
// require explicit tool-call IDs.
func TestGolden_AgentTools_Helpers_Emitted(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportsSimple())
	content := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/search/helpers.go")
	assertGoldenGo(t, "agenttools_helpers", "helpers.go.golden", content)
}
