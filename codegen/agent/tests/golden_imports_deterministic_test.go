package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Deterministic import aliases for custom package user types appear in emitted files.
func TestGolden_Imports_Deterministic(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ImportsDeterministic())
	codecs := fileContent(t, files, "gen/alpha/tools/docs/codecs.go")
	types := fileContent(t, files, "gen/alpha/tools/docs/types.go")
	assertGoldenGo(t, "imports_deterministic", "codecs.go.golden", codecs)
	// Also verify alias mapping via types golden to avoid string checks.
	assertGoldenGo(t, "imports_deterministic", "types.go.golden", types)
}
