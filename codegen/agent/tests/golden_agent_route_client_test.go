package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGoldenAgentDefinitionAndNewClient verifies that callers receive the
// complete generated contract used by the worker.
func TestGoldenAgentDefinitionAndNewClient(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())
	content := fileContent(t, files, "gen/calc/agents/scribe/agent.go")
	assertGoldenGo(t, "agent_route_client", "agent.go.golden", content)
}
