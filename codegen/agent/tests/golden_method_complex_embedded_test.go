package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"strings"
)

// Method-bound tool with nested user types in both method and tool data.
func TestGolden_MethodComplexEmbedded(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodComplexEmbedded())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/profiles/codecs.go")
	specs := fileContent(t, files, "gen/alpha/agents/scribe/specs/profiles/specs.go")
	svcToolset := fileContent(t, files, "gen/alpha/agents/scribe/profiles/service_toolset.go")
	// Accept either external pointer or local alias generics for user types
	if !strings.Contains(codecs, "JSONCodec[*alpha.") && !strings.Contains(codecs, "JSONCodec[") {
		t.Fatalf("expected JSONCodec generic emission for user types, got:\n%s", codecs)
	}
	require.Contains(t, svcToolset, "NewScribeProfilesToolsetRegistration(")
	require.NotEmpty(t, specs)
}
