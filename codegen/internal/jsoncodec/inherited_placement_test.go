// These tests obtain complete original layouts from either root in one Goa
// generation. Original-value codecs must keep shared children and local copies.
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

func TestInheritedStandaloneLayoutsAcrossRoots(t *testing.T) {
	for _, queryRoot := range []int{0, 1} {
		var child, entry expr.UserType
		first := codegen.RunDSL(t, func() {
			dsl.API("first", func() {})
			child = dsl.Type("Child", func() {
				dsl.Attribute("value", dsl.String)
				dsl.Attribute("next", "Child")
				dsl.Required("value")
			})
			entry = dsl.Type("Entry", func() {
				dsl.OneOf("choice", func() {
					dsl.TypeName("Selection")
					dsl.Attribute("text", dsl.String)
					dsl.Attribute("child", child)
					dsl.Attribute("words", dsl.ArrayOf(dsl.String))
					dsl.Attribute("table", dsl.MapOf(dsl.String, dsl.ArrayOf(dsl.String)))
				})
				dsl.Required("choice")
			})
			dsl.Type("SharedRoot", func() {
				dsl.Meta("struct:pkg:path", "shared/types")
				dsl.Meta("type:generate:force")
				dsl.Attribute("entry", entry)
			})
			dsl.Service("catalog", func() {})
		})
		second := codegen.RunDSL(t, func() {
			dsl.API("second", func() {})
			local := dsl.Type("Local", func() {
				dsl.Attribute("child", child)
				dsl.Attribute("children", dsl.ArrayOf(child))
				dsl.Attribute("by_name", dsl.MapOf(dsl.String, child))
				dsl.Attribute("entries", dsl.ArrayOf(entry))
				dsl.Required("child", "entries")
			})
			for _, name := range []string{"alpha", "beta"} {
				dsl.Service(name, func() {
					dsl.Method("exchange", func() { dsl.Payload(local) })
					dsl.Method("child", func() { dsl.Payload(child) })
				})
			}
		})
		generation, err := codegen.NewGeneration("codec.local/gen", []eval.Root{first, second})
		require.NoError(t, err)
		services, err := service.NewPlans(generation,
			service.PlanInput{Root: first, Examples: expr.NewExampleGenerator(first.API.RandomizerFactory)},
			service.PlanInput{Root: second, Examples: expr.NewExampleGenerator(second.API.RandomizerFactory)},
		)
		require.NoError(t, err)
		codecs, err := NewPlan(generation, services[queryRoot])
		require.NoError(t, err)
		require.NoError(t, generation.Freeze())
		for _, plan := range services {
			require.NoError(t, plan.Link())
		}
		files, err := service.Files(services...)
		require.NoError(t, err)
		files, err = codecs.Files(files)
		require.NoError(t, err)
		shared := generatedSource(t, files, "gen/shared/types")
		require.Contains(t, shared, "func EncodeChild(")
		for _, name := range []string{"alpha", "beta"} {
			local := generatedSource(t, files, "gen/"+name)
			require.Contains(t, local, "func EncodeLocal(")
			require.NotContains(t, local, "type Child struct")
		}
		root := compileModule(t, files)
		fixture, err := os.ReadFile("testdata/inherited_placement_test.go")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "gen/alpha/inherited_test.go"), fixture, 0o600)) // #nosec G703 -- compileModule creates a fresh directory; this fixture filename is fixed.
		runGo(t, root, "test", "-count=1", "./gen/...")
		require.NotContains(t, child.Attribute().Meta, "struct:pkg:path")
		require.NotContains(t, entry.Attribute().Meta, "struct:pkg:path")
	}
}
