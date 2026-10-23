// These tests generate original Go values and check their declared fields
// without requiring callers to encode a JSON document.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
)

func TestGeneratedStandaloneValueValidation(t *testing.T) {
	files, err := generate(t, func() {
		version := dsl.Type("SemVer", dsl.String, func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Pattern(`^v?\d+\.\d+\.\d+$`)
		})
		search := located("Search", func() {
			dsl.Attribute("Length", dsl.Int, func() { dsl.Minimum(1) })
			dsl.Attribute("Terms", dsl.MapOf(dsl.String, dsl.Int), func() {
				dsl.MinLength(1)
				dsl.Elem(func() { dsl.Minimum(1) })
			})
			dsl.Required("Length", "Terms")
		})
		untagged := located("Untagged", func() {
			dsl.Attribute("Index", dsl.Int, func() { dsl.Minimum(0) })
			dsl.Attribute("Kind", dsl.String, func() { dsl.Enum("string", "object") })
			dsl.Required("Index", "Kind")
		})
		field := located("Field", func() {
			dsl.OneOf("Branch", func() {
				dsl.Attribute("tagged", dsl.String)
				dsl.Attribute("untagged", untagged)
			})
			dsl.Required("Branch")
		})
		metadata := located("Metadata", func() {
			dsl.Attribute("Title", dsl.String, func() { dsl.MinLength(1) })
			dsl.Attribute("Search", search)
			dsl.Attribute("Fields", dsl.ArrayOf(field))
			dsl.Required("Title", "Search")
		})
		tool := located("Tool", func() {
			dsl.Attribute("Name", dsl.String, func() { dsl.MinLength(1) })
			dsl.Attribute("Schema", dsl.Bytes, func() { dsl.MinLength(1) })
			dsl.Attribute("Metadata", metadata)
			dsl.Required("Name", "Schema")
		})
		definition := located("Definition", func() {
			dsl.Attribute("Version", version)
			dsl.Attribute("Tools", dsl.ArrayOf(tool), func() { dsl.MinLength(1) })
			dsl.Required("Tools")
		})
		selection := located("Selection", func() {
			dsl.Attribute("Name", dsl.String, func() { dsl.MinLength(1) })
			dsl.Required("Name")
		})
		located("LookupResult", func() {
			dsl.Attribute("Selection", selection)
			dsl.Attribute("Definition", definition)
			dsl.Required("Selection", "Definition")
		})
	})
	require.NoError(t, err)
	require.Contains(t, generatedSource(t, files, "gen/types"), "func ValidateLookupResultValue(")
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/value_validation_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/field_validation_test.go"), source, 0o600)) // #nosec G703 -- root belongs to this generated fixture.

	runGo(t, root, "test", "-race", "-count=1", "-v", "./gen/...")
}
