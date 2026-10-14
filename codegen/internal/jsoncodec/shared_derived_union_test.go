// These tests retain both generated representations of an inherited union.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

func TestSharedAndDerivedUnionInOneValue(t *testing.T) {
	files, err := generateSharedDerivedProof(t, func() {
		detail := dsl.Type("Detail", func() {
			dsl.Meta("struct:pkg:path", "shared")
			dsl.Attribute("next", "Detail")
			dsl.Attribute("text", dsl.String)
			dsl.Required("text")
		})
		base := dsl.Type("Envelope", func() {
			dsl.Meta("struct:pkg:path", "shared")
			dsl.OneOf("choice", func() {
				dsl.TypeName("Selection")
				dsl.Attribute("text", dsl.String)
				dsl.Attribute("detail", detail)
			})
			dsl.Required("choice")
		})
		derived := dsl.Type("DerivedEnvelope", func() { dsl.Extend(base) })
		combined := dsl.Type("Combined", func() {
			dsl.Attribute("original", base)
			dsl.Attribute("derived", derived)
			dsl.Required("original", "derived")
		})
		dsl.Service("exchange", func() {
			dsl.Method("send", func() { dsl.Payload(combined) })
		})
	})
	require.NoError(t, err)
	root := compileModule(t, files)
	fixture, err := os.ReadFile("testdata/shared_derived_union_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen/exchange/shared_derived_union_test.go"), fixture, 0o600)) // #nosec G703 -- fixed filename in this test module.
	runGo(t, root, "test", "-count=1", "-v", "./gen/...")
}

func generateSharedDerivedProof(t *testing.T, design func()) ([]*codegen.File, error) {
	t.Helper()
	eval.Reset()
	expr.Root = new(expr.RootExpr)
	expr.GeneratedResultTypes = new(expr.ResultTypesRoot)
	require.NoError(t, eval.Register(expr.Root))
	require.NoError(t, eval.Register(expr.GeneratedResultTypes))
	dsl.API("codec", func() {})
	dsl.Service("catalog", func() {})
	design()
	require.NoError(t, eval.RunDSL())
	roots, err := eval.Context.Roots()
	require.NoError(t, err)
	generation, err := codegen.NewGeneration("codec.local/gen", roots)
	require.NoError(t, err)
	services, err := service.NewPlan(expr.Root, generation, expr.NewExampleGenerator(expr.Root.API.RandomizerFactory))
	require.NoError(t, err)
	codecs, err := NewPlan(generation, services)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, services.Link())
	for original, declaration := range generation.UserTypes() {
		if original.Name() != "Combined" {
			continue
		}
		attribute, layout, err := services.UserTypeLayout(original, declaration)
		require.NoError(t, err)
		base := expr.AsObject(attribute.Type).Attribute("original")
		union := expr.AsObject(base.Type).Attribute("choice").Type.(*expr.Union)
		branch := union.Values[0].Attribute
		alternatives := layout.PlansForOccurrence(branch)
		require.Len(t, alternatives, 2)
		owner := generation.Package(declaration.PackagePath())
		refs := make([]string, 0, len(alternatives))
		for _, alternative := range alternatives {
			ref := alternative.Link(owner.ImportPath(), owner.ImportName).Ref()
			t.Logf("retained branch declaration: owner=%s reference=%s", alternative.Owner(), ref)
			refs = append(refs, ref)
		}
		require.NotEqual(t, refs[0], refs[1])
	}
	files, err := service.Files(services)
	require.NoError(t, err)
	return codecs.Files(files)
}
