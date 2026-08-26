// Package tests checks generated agent code against reviewed output files.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Required primitive-alias arrays and recursive objects should emit direct
// validator calls without a runtime schema interpreter.
func TestGolden_CodecCollections(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.CodecCollections())
	codecs := fileContent(t, files, "gen/alpha/toolsets/collections/codecs.go")
	require.NotContains(t, codecs, "validateGeneratedJSONValue")
	require.NotContains(t, codecs, "FieldAllowedObjectKeys")
	require.Contains(t, codecs, "func validateAnyJSONValue(path string, value any, description string) error")
	require.Contains(t, codecs, `case "aliases":`)
	require.Contains(t, codecs, "validateStorePayloadNodeTransportJSONValue(")
	assertGoldenGo(t, "codec_collections", "codecs.go.golden", codecs)
	assertGoldenGo(
		t,
		"codec_collections",
		"types.go.golden",
		fileContent(t, files, "gen/alpha/toolsets/collections/types.go"),
	)
}
