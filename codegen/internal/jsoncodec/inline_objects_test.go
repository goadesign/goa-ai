// These tests compile original-value codecs with anonymous object fields and
// collection members. Goa owns their pointer layout and required-field checks.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestGeneratedInlineObjects(t *testing.T) {
	files, err := generate(t, func() {
		fields := func() {
			dsl.Attribute("text", dsl.String)
		}
		located("InlineRoot", func() {
			dsl.Attribute("details", fields)
			dsl.Attribute("items", dsl.ArrayOf(&expr.Object{}, fields))
			dsl.Attribute("lookup", dsl.MapOf(dsl.String, &expr.Object{}, func() {
				dsl.Elem(fields)
			}))
		})
		located("RequiredInlineRoot", func() {
			dsl.Attribute("details", fields)
			dsl.Required("details")
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	source, err := os.ReadFile("testdata/inline_objects_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/inline_objects_test.go"), source, 0o600)) // #nosec G703 -- root is this generated fixture's private directory.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}
