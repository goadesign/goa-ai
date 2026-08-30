// Package tests verifies that generated HTTP validators import every package
// required by the exact validation functions written to the file.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGoldenNestedValidationImports compiles a nested string length validator.
// The nested validator uses unicode/utf8 even though the top-level validator
// only calls the nested validation function.
func TestGoldenNestedValidationImports(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.NestedValidationImports())
	validation := renderedFileContent(t, files, "gen/alpha/toolsets/nested/http/validate.go")

	require.Contains(t, validation, `"unicode/utf8"`)
	runCompleteGeneratedPackageTest(t, files, "./gen/alpha/toolsets/nested/...")
	assertGoldenGo(t, "nested_validation_imports", "transport_validate.go.golden", validation)
}
