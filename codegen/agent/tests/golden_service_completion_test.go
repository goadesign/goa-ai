package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Service-owned completions should generate a dedicated completions package
// without requiring an agent or toolset wrapper.
func TestGolden_ServiceCompletion(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceCompletion())

	types := fileContent(t, files, "gen/tasks/completions/types.go")
	unions := fileContent(t, files, "gen/tasks/completions/unions.go")
	codecs := fileContent(t, files, "gen/tasks/completions/codecs.go")
	specs := fileContent(t, files, "gen/tasks/completions/specs.go")

	assertGoldenGo(t, "service_completion", "types.go.golden", types)
	assertGoldenGo(t, "service_completion", "unions.go.golden", unions)
	assertGoldenGo(t, "service_completion", "codecs.go.golden", codecs)
	assertGoldenGo(t, "service_completion", "specs.go.golden", specs)
}
