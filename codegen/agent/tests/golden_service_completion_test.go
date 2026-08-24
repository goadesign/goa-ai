package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
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

	require.Contains(t, specs, "func specDraftFromTranscript() completion.Spec")
	require.Contains(t, specs, "func DraftFromTranscriptExample() rawjson.Message")
	require.Contains(t, specs, "func CompleteDraftFromTranscript(")
	require.Contains(t, specs, "func StreamCompleteDraftFromTranscript(")
	require.NotContains(t, specs, "SpecDraftFromTranscript")
	require.Contains(t, codecs, "func newDraftFromTranscriptResultCodec(")
	require.Contains(t, codecs, "func marshalDraftFromTranscriptResult(")
	require.Contains(t, codecs, "func unmarshalDraftFromTranscriptResult(")
	require.NotContains(t, codecs, "func DraftFromTranscriptResultCodec(")
	require.NotContains(t, codecs, "func MarshalDraftFromTranscriptResult(")
	require.NotContains(t, codecs, "func UnmarshalDraftFromTranscriptResult(")
}
