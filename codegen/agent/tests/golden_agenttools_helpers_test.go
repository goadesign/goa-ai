package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGoldenAgentToolsHelpersEmitted checks the complete helper file generated
// for an exported toolset.
func TestGolden_AgentTools_Helpers_Emitted(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportsSimple())
	content := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/search/helpers.go")
	require.Contains(t, content, "func NewFindCall(")
	require.NotContains(t, content, "func NewSearchFindCall(")
	assertGoldenGo(t, "agenttools_helpers", "helpers.go.golden", content)
}

// TestGoldenAgentToolsWithoutResult checks that a tool with no result emits no
// result alias and that the complete generated package compiles.
func TestGoldenAgentToolsWithoutResult(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.ExportsNoResult())
	content := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/maintenance/helpers.go")
	specs := fileContent(t, files, "gen/alpha/agents/scribe/exports/maintenance/specs.go")
	require.NotContains(t, content, "PurgeResult")
	require.Contains(t, content, "func NewPurgeCall(args PurgePayload)")
	require.Contains(t, specs, "func PurgeTool() tools.TypedTool[PurgePayload, any]")
	assertGoldenGo(t, "agenttools_no_result", "helpers.go.golden", content)
	runCompleteGeneratedPackageTest(t, files, "./gen/alpha/agents/scribe/agenttools/maintenance/...")
}
