package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verify that agenttools helpers are emitted for exported toolsets with typed
// New<Tool>Call and CallOption helpers.
func TestGolden_AgentTools_Helpers_Emitted(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportsSimple())
	content := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/search/helpers.go")
	require.Contains(t, content, "package search")
	require.Contains(t, content, "type CallOption func(*planner.ToolRequest)")
	require.Contains(t, content, "func WithParentToolCallID(")
	require.Contains(t, content, "func WithToolCallID(")
	require.Contains(t, content, "func NewFindCall(")
	require.Contains(t, content, "planner.ToolRequest")
	// Aliases and codec re-exports should be present
	require.Contains(t, content, "type FindPayload =")
	require.Contains(t, content, "var FindPayloadCodec =")
}
