package codegen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
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
		// Provider service referenced by FromMCP
		Service("assistant", func() {})
		assistantSuite := Toolset(FromMCP("assistant", "assistant-mcp"))
		Service("svc", func() {
			Agent("a", "", func() {
				Use(assistantSuite)
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
