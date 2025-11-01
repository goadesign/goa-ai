package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
)

func TestGolden_Args_Primitive(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsPrimitive())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/ops/codecs.go")
	require.Contains(t, codecs, "JSONCodec[string]")
	require.Contains(t, codecs, "func MarshalEchoPayload(v string)")
}

func TestGolden_Args_InlineObject(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsInlineObject())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/math/codecs.go")
	require.NotEmpty(t, codecs)
}

func TestGolden_Args_UserType(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ArgsUserType())
	// user type variants typically do not emit pure types; check codecs/specs
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/docs/codecs.go")
	require.Contains(t, codecs, "JSONCodec[*alpha.")
}
