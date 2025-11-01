package codegen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agents"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Ensures external MCP toolsets are materialized as self-contained types (no aliases).
func TestExternalMCPToolset_SelfContainedTypes(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		API("svc", func() {})
		// Provider service referenced by UseMCPToolset
		Service("assistant", func() {})
		Service("svc", func() {
			Agent("a", "", func() {
				Uses(func() { UseMCPToolset("assistant", "assistant-mcp") })
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	data, err := codegen.BuildDataForTest("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root})
	require.NoError(t, err)
	require.NotNil(t, data)
	svc := data.Services[0]
	ag := svc.Agents[0]
	specs, err := codegen.BuildToolSpecsDataForTest(ag)
	require.NoError(t, err)

	defs := codegen.CollectTypeInfoForTest(specs)
	// Look for assistant-mcp types; ensure no "= <pkg>." aliasing appears.
	for name, def := range defs {
		if strings.Contains(name, "AssistantMcp") {
			require.NotContainsf(t, def, "= ", "unexpected alias in %s: %s", name, def)
		}
	}
}
