// These tests verify that method-backed request helpers use the payload shape
// selected by Goa instead of forcing every argument to be a pointer.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenUsedToolPayloadShapes(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.MethodPayloadShapes())
	path := "gen/alpha/agents/scribe/helpers/used_tools.go"
	content := renderedFileContent(t, files, path)

	require.Contains(t, content, "func NewEchoCall(args EchoPayload)")
	require.Contains(t, content, "func NewJoinCall(args JoinPayload)")
	require.Contains(t, content, "func NewFormatCall(args FormatPayload)")
	assertGoldenGo(t, "used_tool_payload_shapes", "used_tools.go.golden", content)
	runCompleteGeneratedPackageTest(t, files, "./gen/alpha/agents/scribe/helpers/...")
}
