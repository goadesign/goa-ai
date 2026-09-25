// Recursive collection fixtures check termination before generated transforms.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

func TestRecursiveCollection(t *testing.T) {
	files, err := generate(t, func() {
		var tree expr.UserType
		tree = dsl.Type("Tree", dsl.MapOf(dsl.String, dsl.String), func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta(selectionKey)
			// Resolve the recursive element after the declaration exists.
			tree.Attribute().Type.(*expr.Map).ElemType.Type = tree
		})
		var list expr.UserType
		list = dsl.Type("List", dsl.ArrayOf(dsl.String), func() {
			dsl.Meta("struct:pkg:path", "types")
			dsl.Meta("type:generate:force")
			dsl.Meta(selectionKey)
			list.Attribute().Type.(*expr.Array).ElemType.Type = list
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	fixture, err := os.ReadFile(filepath.Join("testdata", "recursive_collection_test.go.txt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/jsoncodec/collection_test.go"), fixture, 0o600)) // #nosec G703 -- root is the generated fixture's private directory.
	runGo(t, root, "test", "-timeout=3s", "./gen/...")
}
