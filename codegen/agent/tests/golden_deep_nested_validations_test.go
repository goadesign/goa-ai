package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Deeply nested user types with validations at each level should emit
// validators for every user type referenced by the payload and wire
// validation errors to ValidationError for typed correction recovery.
func TestGolden_DeepNested_Validations(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.DeepNestedValidations())
	codecs := fileContent(t, files, "gen/alpha/toolsets/deep/codecs.go")
	require.NotContains(t, codecs, "validateGeneratedJSONValue")
	require.Contains(t, codecs, `case "child":`)
	assertGoldenGo(t, "deep_nested_validations", "codecs.go.golden", codecs)
}
