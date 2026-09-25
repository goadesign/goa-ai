// These fixtures exercise nested collections through their generated public
// codecs, preserving each element's nullability and union branch validation.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedNestedCollections(t *testing.T) {
	files, err := generate(t, func() {
		entry := located("Entry", func() {
			dsl.Attribute("Label", dsl.String)
			dsl.Required("Label")
		})
		for _, test := range []struct {
			name  string
			value any
		}{
			{"NestedEntries", dsl.ArrayOf(dsl.ArrayOf(entry))},
			{"RequiredRows", dsl.ArrayOfRequired(dsl.ArrayOf(entry))},
			{"Matrix", dsl.ArrayOf(dsl.ArrayOf(dsl.String))},
			{"EntryMap", dsl.MapOf(dsl.String, entry)},
			{"RowMap", dsl.MapOf(dsl.String, dsl.ArrayOf(dsl.String))},
			{"BlobMap", dsl.MapOf(dsl.String, dsl.Bytes)},
			{"LabelMap", dsl.MapOf(dsl.String, dsl.String)},
		} {
			dsl.Type(test.name, test.value, func() {
				dsl.Meta("struct:pkg:path", "types")
				dsl.Meta("type:generate:force")
				dsl.Meta(selectionKey)
			})
		}
		dsl.Type("Choices", dsl.ArrayOf(&expr.Union{TypeName: "Choice"}, func() {
			dsl.Attribute("text", dsl.String)
			dsl.Attribute("entry", entry)
		}), func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta(selectionKey)
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	fixture, err := os.ReadFile("testdata/nested_collections_test.go.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/jsoncodec/nested_collections_test.go"), fixture, 0o600)) // #nosec G703 -- root is the generated fixture's private directory.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}
