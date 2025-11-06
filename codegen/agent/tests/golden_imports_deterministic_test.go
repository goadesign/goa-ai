package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Deterministic import aliases for custom package user types appear in codecs.go.
func TestGolden_Imports_Deterministic(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ImportsDeterministic())
	codecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/docs/codecs.go")
	require.Contains(t, codecs, "goa.design/goa-ai/gen/example.com/mod/gen/types")
	// The codec uses the alias type (StorePayload) which aliases to types.Doc
	require.Contains(t, codecs, "JSONCodec[StorePayload]")
	// The type alias is defined in types.go, verify it's used in codecs.go
	types := fileContent(t, files, "gen/alpha/agents/scribe/specs/docs/types.go")
	require.Contains(t, types, "StorePayload = types.Doc")
}
