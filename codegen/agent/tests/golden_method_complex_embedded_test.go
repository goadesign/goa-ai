package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Method-bound tool with nested user types in both method and tool data.
func TestGolden_MethodComplexEmbedded(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodComplexEmbedded())
	types := fileContent(t, files, "gen/alpha/tools/profiles/types.go")
	codecs := fileContent(t, files, "gen/alpha/tools/profiles/codecs.go")
	specs := fileContent(t, files, "gen/alpha/tools/profiles/specs.go")
	assertGoldenGo(t, "method_complex_embedded", "types.go.golden", types)
	assertGoldenGo(t, "method_complex_embedded", "codecs.go.golden", codecs)
	assertGoldenGo(t, "method_complex_embedded", "specs.go.golden", specs)
}
