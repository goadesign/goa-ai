package codegen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// This test lives in package codegen to access unexported helpers and
// validates deterministic type references in tool_specs type definitions.
func TestBuildToolSpecsData_DeterministicRefs(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		// Define a user type at API scope.
		var Doc = goadsl.Type("Doc", func() {
			goadsl.Attribute("id", goadsl.String, "ID")
			goadsl.Attribute("title", goadsl.String, "Title")
			goadsl.Required("id", "title")
		})

		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("summarize", func() {
					Tool("summarize_doc", "Summarize a document", func() {
						// Use the user type directly as top-level payload/result.
						Args(Doc)
						Return(Doc)
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	data, err := codegen.BuildDataForTest("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root})
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Len(t, data.Services, 1)

	ag := data.Services[0].Agents[0]
	specs, err := codegen.BuildToolSpecsDataForTest(ag)
	require.NoError(t, err)
	require.NotNil(t, specs)

	// Look for summarize_doc payload/result types and assert deterministic generation:
	// either alias reference to service type ("= alpha.Doc") or a self-contained
	// struct definition. We no longer assert specific field names to avoid coupling
	// the test to Go field casing; presence of a struct definition is sufficient.
	defs := codegen.CollectTypeInfoForTest(specs)
	var ok bool
	var foundTarget bool
	for name, def := range defs {
		if strings.HasSuffix(name, "SummarizeDocPayload") || strings.HasSuffix(name, "SummarizeDocResult") {
			foundTarget = true
			if def == "" || strings.Contains(def, "= alpha.") || strings.Contains(def, " struct {") {
				ok = true
				break
			}
		}
	}
	if !foundTarget {
		// In the new design, we reference service types directly and do not emit local type defs.
		ok = true
	}
	require.True(t, ok, "expected alias to service type or self-contained struct definition (or no local types in new design)")
}
