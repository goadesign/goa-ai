// These tests verify the complete generated helper file when tool names collide
// with fixed package declarations or with one another.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenAgentToolsNameCollisions(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ExportedToolNameCollisions())
	content := fileContent(t, files, "gen/alpha/agents/scribe/agenttools/helpers/helpers.go")
	require.Contains(t, content, "func NewAgentIDCall(")
	require.Contains(t, content, "func NewAgentIDCall2(")
	require.Contains(t, content, "func NewServiceCall(")
	require.NotContains(t, content, "func NewHelpersAgentIDCall(")
	assertGoldenGo(t, "agenttools_name_collisions", "helpers.go.golden", content)
}
