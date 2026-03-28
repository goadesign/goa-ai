package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Primitive completion results should reuse the shared codec template without
// importing transport helpers that are only needed for object decoding.
func TestGolden_ServiceCompletionPrimitive(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ServiceCompletionPrimitive())

	require.False(t, fileExists(files, "gen/tasks/completions/http/types.go"))

	types := fileContent(t, files, "gen/tasks/completions/types.go")
	codecs := fileContent(t, files, "gen/tasks/completions/codecs.go")
	specs := fileContent(t, files, "gen/tasks/completions/specs.go")

	assertGoldenGo(t, "service_completion_primitive", "types.go.golden", types)
	assertGoldenGo(t, "service_completion_primitive", "codecs.go.golden", codecs)
	assertGoldenGo(t, "service_completion_primitive", "specs.go.golden", specs)
}
