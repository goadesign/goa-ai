package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
)

// Method-bound tool with nested user types in both method and tool data.
func TestGolden_MethodComplexEmbedded(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodComplexEmbedded())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/profiles/codecs.go")
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/profiles/specs.go")
	svcToolset := fileContent(t, files, "gen/alpha/agents/scribe/profiles/service_toolset.go")
	require.Contains(t, codecs, "JSONCodec[*alpha.")
	require.Contains(t, svcToolset, "NewScribeProfilesToolsetRegistration(")
	require.NotEmpty(t, specs)
}
