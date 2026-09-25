// These tests generate protocol and original-value codecs from the same service
// declarations so union collection values and object pointers stay compatible.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestGeneratedUnionCollectionCodecs(t *testing.T) {
	eval.Reset()
	expr.Root = new(expr.RootExpr)
	expr.GeneratedResultTypes = new(expr.ResultTypesRoot)
	require.NoError(t, eval.Register(expr.Root))
	require.NoError(t, eval.Register(expr.GeneratedResultTypes))
	dsl.API("codec", func() {})
	dsl.Service("catalog", func() {})
	entry := located("CollectionEntry", func() {
		dsl.Attribute("Label", dsl.String, func() { dsl.MinLength(1) })
		dsl.Attribute("Note", dsl.String)
		dsl.Required("Label")
	})
	collections := located("UnionCollections", func() {
		dsl.Attribute("choices", dsl.ArrayOf(&expr.Union{TypeName: "ArrayChoice"}, func() {
			dsl.Attribute("text", dsl.String, func() { dsl.Pattern("^[a-z]*$") })
			dsl.Attribute("entry", entry)
		}))
		dsl.Attribute("byName", dsl.MapOf(dsl.String, &expr.Union{TypeName: "MapChoice"}, func() {
			dsl.Elem(func() {
				dsl.Attribute("text", dsl.String, func() { dsl.Pattern("^[a-z]*$") })
				dsl.Attribute("entry", entry)
			})
		}))
		dsl.Attribute("objects", dsl.ArrayOf(entry))
		dsl.Attribute("objectsByName", dsl.MapOf(dsl.String, entry))
		dsl.Required("choices", "byName", "objects", "objectsByName")
	})
	require.NoError(t, eval.RunDSL())
	roots, err := eval.Context.Roots()
	require.NoError(t, err)
	generation, err := codegen.NewGeneration("codec.local/gen", roots)
	require.NoError(t, err)
	services, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	require.NoError(t, err)

	// Both producers register before freeze and use Goa's original declarations.
	originals, err := NewPlan(generation)
	require.NoError(t, err)
	const ordinaryPath = "codec.local/gen/ordinary"
	ordinary, err := codec.NewPlan(generation, ordinaryPath, "codec.local/gen/types")
	require.NoError(t, err)
	value, err := ordinary.Add("union-collections", "UnionCollections",
		&expr.AttributeExpr{Type: collections}, codec.EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, services.Link())
	require.NoError(t, value.BindService(services.Services().ServiceAttributor("catalog", ordinaryPath)))
	files, err := service.Files(services)
	require.NoError(t, err)
	files, err = originals.Files(files)
	require.NoError(t, err)
	ordinaryFiles, err := ordinary.Files("ordinary")
	require.NoError(t, err)
	files = append(files, ordinaryFiles...)

	root := compileModule(t, files)
	fixture, err := os.ReadFile("testdata/union_collections_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/union_collections_test.go"), fixture, 0o600)) // #nosec G703 -- root is the test fixture's private directory.
	runGo(t, root, "test", "-count=1", "-run", "^TestUnionCollections", "./gen/...")
}
