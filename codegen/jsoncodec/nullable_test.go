// These tests use the same generated element type in nullable and nonnullable
// arrays, and compile the public codecs against Goa's existing declarations.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
)

func TestGeneratedNullableArrayRoots(t *testing.T) {
	files, err := generate(t, func() {
		entry := located("Entry", func() {
			dsl.Attribute("Label", dsl.String)
			dsl.Required("Label")
		})
		for _, test := range []struct {
			name  string
			value any
		}{
			{"Entries", dsl.ArrayOf(entry)},
			{"RequiredEntries", dsl.ArrayOfRequired(entry)},
			{"Words", dsl.ArrayOf(dsl.String)},
			{"RequiredWords", dsl.ArrayOfRequired(dsl.String)},
			{"Blobs", dsl.ArrayOf(dsl.Bytes)},
			{"RequiredBlobs", dsl.ArrayOfRequired(dsl.Bytes)},
		} {
			dsl.Type(test.name, test.value, func() {
				dsl.Meta("struct:pkg:path", "types")
				dsl.Meta("type:generate:force")
				dsl.Meta(selectionKey)
			})
		}
		located("Container", func() {
			dsl.Meta(selectionKey)
			dsl.Attribute("Single", entry)
			dsl.Attribute("Loose", dsl.ArrayOf(entry))
			dsl.Attribute("Tight", dsl.ArrayOfRequired(entry))
			dsl.OneOf("Choice", func() {
				dsl.Attribute("entry", entry)
				dsl.Attribute("text", dsl.String)
			})
			dsl.Required("Single", "Loose", "Tight", "Choice")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/nullable_test.go.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/jsoncodec/nullable_test.go"), source, 0o600)) // #nosec G703 -- root is this test's private temporary directory.
	runGo(t, root, "test", "-count=1", "./gen/...")
}
