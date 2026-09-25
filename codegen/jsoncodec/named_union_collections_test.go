// These tests compile both codec entry points for named union values whose
// branch methods belong to a declaration in another generated package.
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

func TestGeneratedNamedUnionCollections(t *testing.T) {
	eval.Reset()
	expr.Root = new(expr.RootExpr)
	expr.GeneratedResultTypes = new(expr.ResultTypesRoot)
	require.NoError(t, eval.Register(expr.Root))
	require.NoError(t, eval.Register(expr.GeneratedResultTypes))
	dsl.API("codec", func() {})
	dsl.Service("catalog", func() {})
	base := dsl.Type("Base", &expr.Union{TypeName: "Choice"}, func() {
		dsl.Meta("struct:pkg:path", "left/types")
		dsl.Meta(selectionKey)
		dsl.Attribute("text", dsl.String)
		dsl.Attribute("number", dsl.Int, func() { dsl.Minimum(1) })
	})
	derived := dsl.Type("Derived", base, func() {
		dsl.Meta("struct:pkg:path", "right/types")
		dsl.Meta(selectionKey)
	})
	collections := located("NamedCollections", func() {
		dsl.Meta(selectionKey)
		dsl.Attribute("single", derived)
		dsl.Attribute("choices", dsl.ArrayOf(derived))
		dsl.Attribute("byName", dsl.MapOf(dsl.String, derived))
		dsl.Required("single", "choices", "byName")
	})
	require.NoError(t, eval.RunDSL())
	roots, err := eval.Context.Roots()
	require.NoError(t, err)
	generation, err := codegen.NewGeneration("codec.local/gen", roots)
	require.NoError(t, err)
	services, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	require.NoError(t, err)
	selected := new(plugin)
	require.NoError(t, selected.plan(generation))
	const ordinaryPath = "codec.local/gen/ordinary"
	ordinary, err := codec.NewPlan(generation, ordinaryPath, "ordinary", "codec.local/gen/types")
	require.NoError(t, err)
	values := make([]*codec.Value, 0, 3)
	for _, named := range []expr.UserType{base, derived, collections} {
		value, err := ordinary.Add(named.Name(), named.Name(),
			&expr.AttributeExpr{Type: named}, codec.EncodeAndDecode)
		require.NoError(t, err)
		values = append(values, value)
	}
	require.NoError(t, generation.Freeze())
	require.NoError(t, services.Link())
	for _, value := range values {
		require.NoError(t, value.BindService(services.Services().ServiceAttributor("catalog", ordinaryPath)))
	}
	files, err := service.Files(services)
	require.NoError(t, err)
	files, err = selected.generate(files)
	require.NoError(t, err)
	ordinaryFiles, err := ordinary.Files()
	require.NoError(t, err)
	files = append(files, ordinaryFiles...)

	root := compileModule(t, files)
	fixture, err := os.ReadFile("testdata/named_union_collections_test.go.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/types/jsoncodec/named_union_collections_test.go"), fixture, 0o600)) // #nosec G703 -- root is the generated fixture's private directory.
	runGo(t, root, "test", "-count=1", "-run", "^TestNamedUnionCollections", "./gen/...")
}
