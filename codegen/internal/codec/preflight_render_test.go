// Package codec checks that complete-value codecs keep every generated check
// in insertion order and measures linking as the number of values grows.
package codec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

func TestPlanPreflightsKeepValueOrder(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("values_%d", count), func(t *testing.T) {
			plan := preflightRenderTestPlan(t, count)
			checks := make([]string, 0, count)
			for _, value := range plan.values {
				check, err := value.renderPreflight()
				require.NoError(t, err)
				checks = append(checks, check)
			}
			want := strings.Join(checks, "")
			files, err := plan.Files("types")
			require.NoError(t, err)
			require.Len(t, files, 1)
			if count == 0 {
				assert.Len(t, files[0].SectionTemplates, 2)
				return
			}
			require.Len(t, files[0].SectionTemplates, 3)
			assert.Equal(t, want, files[0].SectionTemplates[2].Data.(*fileData).Preflights)
			directory := t.TempDir()
			_, err = files[0].Render(directory)
			require.NoError(t, err)
			source := readOnlyGoFile(t, directory)
			position := -1
			for _, value := range plan.values {
				declaration := "func " + value.standalone.preflight.Name() + "("
				assert.Equal(t, 1, strings.Count(source, declaration))
				next := strings.Index(source, declaration)
				assert.Greater(t, next, position)
				position = next
			}
			files, err = plan.Files("types")
			assert.Nil(t, files)
			assert.EqualError(t, err, fmt.Sprintf("link JSON value %q encoder: transform contexts are already bound", plan.values[0].key))
		})
	}
}

func TestPlanOrdinaryCodecHasNoPreflights(t *testing.T) {
	generation, err := codegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	plan, err := NewPlan(generation, "example.com/gen/codec")
	require.NoError(t, err)
	value, err := addCodecTestValue(t, plan, "message", "Message", &expr.AttributeExpr{Type: expr.String}, EncodeAndDecode)
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	context := codegen.NewAttributeContext(false, false, true, "", codegen.NewNameScope())
	require.NoError(t, value.BindService(context.Scope))
	files, err := plan.Files("codec")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Len(t, files[0].SectionTemplates, 2)
	assert.Empty(t, files[0].SectionTemplates[1].Data.(*fileData).Preflights)
}

func BenchmarkPlanLinkStandalonePreflights(b *testing.B) {
	for _, count := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("values_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				plan := preflightRenderTestPlan(b, count)
				b.StartTimer()
				data, _, err := plan.link()
				if err != nil {
					b.Fatal(err)
				}
				if len(data.Preflights) == 0 {
					b.Fatal("standalone values generated no checks")
				}
			}
		})
	}
}

// preflightRenderTestPlan adds independent object codecs, freezes their names,
// and binds their original types so tests and benchmarks link a real codec file.
func preflightRenderTestPlan(t testing.TB, count int) *Plan {
	t.Helper()
	types := make([]expr.UserType, count)
	for index := range types {
		types[index] = standaloneTestType(fmt.Sprintf("Entry%04d", count-index), &expr.Object{
			{Name: "title", Attribute: &expr.AttributeExpr{Type: expr.String}},
			{Name: "tags", Attribute: &expr.AttributeExpr{Type: &expr.Array{
				ElemType: &expr.AttributeExpr{Type: expr.String},
			}}},
			{Name: "labels", Attribute: &expr.AttributeExpr{Type: &expr.Map{
				KeyType:  &expr.AttributeExpr{Type: expr.String},
				ElemType: &expr.AttributeExpr{Type: expr.String},
			}}},
		})
	}
	generation, owner, plan := standaloneTestPlan(t, types...)
	for _, userType := range types {
		addStandaloneTestValue(t, generation, owner, plan, userType)
	}
	require.NoError(t, generation.Freeze())
	for _, value := range plan.values {
		context := &codegen.AttributeContext{UseDefault: true, Scope: codegen.NewAttributeScope(owner.Scope())}
		context, err := context.WithGoTypeLayout(value.originalLayout.Link(owner.ImportPath(), owner.ImportName))
		require.NoError(t, err)
		require.NoError(t, value.BindService(context.Scope))
	}
	return plan
}
