package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verify that agent.go emits Route() and NewClient(rt) helpers for caller-only usage.
func TestGolden_Agent_Route_And_NewClient(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())
	content := fileContent(t, files, "gen/calc/agents/scribe/agent.go")
	assertGoldenGo(t, "agent_route_client", "agent.go.golden", content)
}
