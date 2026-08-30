package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Method-bound tool with nested user types in both method and tool data.
func TestGolden_MethodComplexEmbedded(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MethodComplexEmbedded())
	types := fileContent(t, files, "gen/alpha/toolsets/profiles/types.go")
	codecs := fileContent(t, files, "gen/alpha/toolsets/profiles/codecs.go")
	specs := fileContent(t, files, "gen/alpha/toolsets/profiles/specs.go")
	registry := fileContent(t, files, "gen/alpha/agents/scribe/registry.go")
	require.NotContains(t, specs, "Service:")
	require.NotContains(t, specs, "Toolset:")
	require.Contains(t, registry, `const ProfilesToolsetName = "alpha.profiles"`)
	require.Contains(t, registry, "Name:               ProfilesToolsetName")
	assertGoldenGo(t, "method_complex_embedded", "types.go.golden", types)
	assertGoldenGo(t, "method_complex_embedded", "codecs.go.golden", codecs)
	assertGoldenGo(t, "method_complex_embedded", "specs.go.golden", specs)
}
