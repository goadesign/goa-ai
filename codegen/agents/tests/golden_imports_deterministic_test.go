package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
)

// Deterministic import aliases for custom package user types appear in codecs.go.
func TestGolden_Imports_Deterministic(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ImportsDeterministic())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/docs/codecs.go")
	require.Contains(t, codecs, "goa.design/goa-ai/gen/example.com/mod/gen/types")
	require.Contains(t, codecs, "JSONCodec[*types.Doc]")
}
