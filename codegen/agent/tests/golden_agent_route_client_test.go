package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verify that agent.go emits Route() and NewClient(rt) helpers for caller-only usage.
func TestGolden_Agent_Route_And_NewClient(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())
	content := fileContent(t, files, "gen/calc/agents/scribe/agent.go")
	require.Contains(t, content, "const AgentID agent.Ident")
	require.Contains(t, content, "func Route() runtime.AgentRoute")
	require.Contains(t, content, "func NewClient(rt *runtime.Runtime) runtime.AgentClient")
	// Ensure NewClient delegates to route-based client
	require.Contains(t, content, "MustClientFor(Route())")
}
