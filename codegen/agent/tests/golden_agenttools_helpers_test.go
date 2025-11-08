package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verify that agenttools helpers are emitted for exported toolsets with typed
// New<Tool>Call and CallOption helpers.
func TestGolden_AgentTools_Helpers_Emitted(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportsSimple())
	content := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/search/helpers.go")
	assertGoldenGo(t, "agenttools_helpers", "helpers.go.golden", content)
}
