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

	complete := buildCompleteGeneratedFiles(t, testscenarios.ServiceCompletionPrimitive())
	runCompleteGeneratedPackageTest(t, complete, "./gen/tasks/completions/...")
}

// Direct collection completions should import externally located Goa types
// without creating transport-only completion types.
func TestGolden_ServiceCompletionLocatedCollection(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.ServiceCompletionLocatedCollection())

	require.False(t, fileExists(files, "gen/tasks/completions/http/types.go"))

	types := fileContent(t, files, "gen/tasks/completions/types.go")
	codecs := fileContent(t, files, "gen/tasks/completions/codecs.go")
	require.NotContains(t, codecs, "toolhttp")
	assertGoldenGo(t, "service_completion_located_collection", "types.go.golden", types)

	runCompleteGeneratedPackageTest(t, files, "./gen/tasks/completions/...")
}

// Authored result types used as union branches should retain their external
// Goa package while compiler-created primitive branch types stay local.
func TestGolden_ServiceCompletionLocatedResultBranch(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.ServiceCompletionLocatedResultBranch())

	require.True(t, fileExists(files, "gen/types/located_result.go"))
	require.False(t, fileExists(files, "gen/types/selection_message.go"))

	types := renderedFileContent(t, files, "gen/tasks/completions/types.go")
	unions := renderedFileContent(t, files, "gen/tasks/completions/unions.go")
	require.Contains(t, unions, `types "generated.local/gen/types"`)
	require.Contains(t, unions, "*types.LocatedResult")
	assertGoldenGo(t, "service_completion_located_result_branch", "types.go.golden", types)

	runCompleteGeneratedPackageTest(t, files, "./gen/tasks/completions/...")
}
