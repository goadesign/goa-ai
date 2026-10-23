// These tests generate typed union validators and run them against selected
// branches, optional fields, collections, and named types without encoding JSON.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedUnionValueValidation(t *testing.T) {
	files, err := generate(t, func() {
		empty := located("Empty", func() {})
		choice := dsl.Type("Choice", &expr.Union{TypeName: "ChoiceValue"}, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Attribute("empty", empty)
			dsl.Attribute("words", dsl.ArrayOf(dsl.String))
			dsl.Attribute("counts", dsl.MapOf(dsl.String, dsl.Int))
			dsl.Attribute("text", dsl.String)
		})
		derived := dsl.Type("Derived", choice, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
		})
		located("Holder", func() {
			dsl.Attribute("Selected", choice)
			dsl.Attribute("Optional", choice)
			dsl.Attribute("Derived", derived)
			dsl.Attribute("Items", dsl.ArrayOf(choice))
			dsl.Required("Selected")
		})
	})
	require.NoError(t, err)
	source := generatedSource(t, files, "gen/types")
	for _, name := range []string{"ValidateChoiceValue", "ValidateDerivedValue", "ValidateHolderValue"} {
		require.Contains(t, source, "func "+name+"(")
	}
	root := compileModule(t, files)
	input, err := os.ReadFile("testdata/union_value_validation_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/union_value_validation_test.go"), input, 0o600)) // #nosec G703 -- root belongs to this generated fixture.
	runGo(t, root, "test", "-race", "-count=1", "-v", "./gen/...")
}
