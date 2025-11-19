package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Minimal tool specs for an agent with one toolset and one tool with simple args/return.
func TestGolden_ToolSpecs_Minimal(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())
	types := fileContent(t, files, "gen/calc/tools/helpers/types.go")
	codecs := fileContent(t, files, "gen/calc/tools/helpers/codecs.go")
	specs := fileContent(t, files, "gen/calc/tools/helpers/specs.go")
	assertGoldenGo(t, "tool_specs_minimal", "types.go.golden", types)
	assertGoldenGo(t, "tool_specs_minimal", "codecs.go.golden", codecs)
	assertGoldenGo(t, "tool_specs_minimal", "specs.go.golden", specs)
}
