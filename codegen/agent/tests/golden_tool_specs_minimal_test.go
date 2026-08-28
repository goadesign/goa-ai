package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Minimal tool specs for an agent with one toolset and one tool with simple args/return.
func TestGolden_ToolSpecs_Minimal(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())
	types := fileContent(t, files, "gen/calc/toolsets/helpers/types.go")
	codecs := fileContent(t, files, "gen/calc/toolsets/helpers/codecs.go")
	specs := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")
	require.Contains(t, specs, "func Specs() []tools.ToolSpec")
	require.NotContains(t, specs, "var Specs")
	require.Contains(t, specs, "func SpecSummarizeDoc() tools.ToolSpec")
	require.NotContains(t, specs, "var SpecSummarizeDoc")
	require.Contains(t, specs, "func SummarizeDocTool() tools.TypedTool")
	require.Contains(t, specs, "SchemaFingerprint = ")
	require.Contains(t, specs, "func RegistrationToken(admissionRevision string) (string, error)")
	require.NotContains(t, specs, "func NewSummarizeDocCall(")
	require.NotContains(t, specs, "func PayloadSchema(")
	require.NotContains(t, specs, "func ResultSchema(")
	assertGoldenGo(t, "tool_specs_minimal", "types.go.golden", types)
	assertGoldenGo(t, "tool_specs_minimal", "codecs.go.golden", codecs)
	assertGoldenGo(t, "tool_specs_minimal", "specs.go.golden", specs)
}
