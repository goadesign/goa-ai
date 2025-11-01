package codegen_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// Local types: service "alpha" with inline payload/result.
func TestMethodTypeRefs_LocalTypes(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		// Define user types at API scope per Goa DSL rules
		var IDT = goadsl.Type("IDT", func() {
			goadsl.Attribute("id", goadsl.String, "ID")
			goadsl.Required("id")
		})
		var OkT = goadsl.Type("OkT", func() {
			goadsl.Attribute("ok", goadsl.Boolean, "OK")
			goadsl.Required("ok")
		})
		goadsl.Service("alpha", func() {
			goadsl.Method("Find", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("ident", goadsl.String, "Identifier")
					goadsl.Required("ident")
				})
				goadsl.Result(func() {
					goadsl.Attribute("okay", goadsl.Boolean, "OK")
					goadsl.Required("okay")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("lookup", func() {
						Tool("by_id", "Lookup by ID", func() {
							Args(IDT)
							Return(OkT)
							BindTo("Find")
						})
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
	var svc *codegen.ServiceAgentsData
	for _, s := range data.Services {
		if s.Service.Name == "alpha" {
			svc = s
			break
		}
	}
	require.NotNil(t, svc)
	require.NotEmpty(t, svc.Agents)
	ag := svc.Agents[0]
	require.NotEmpty(t, ag.UsedToolsets)
	tool := ag.UsedToolsets[0].Tools[0]
	require.Contains(t, tool.MethodPayloadTypeRef, "*alpha.")
	require.Contains(t, tool.MethodResultTypeRef, "*alpha.")
}

// Custom package types: service "bravo" with Doc user type in custom package.
func TestMethodTypeRefs_CustomPackage(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bravo", func() {})
		var Doc = goadsl.Type("Doc", func() {
			goadsl.Meta("struct:pkg:path", "example.com/mod/gen/types")
			goadsl.Attribute("id", goadsl.String, "Identifier")
			goadsl.Required("id")
		})
		goadsl.Service("bravo", func() {
			goadsl.Method("Store", func() {
				goadsl.Payload(Doc)
				goadsl.Result(Doc)
			})
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("docs", func() {
						Tool("store", "Store", func() {
							Args(Doc)
							Return(Doc)
							BindTo("Store")
						})
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
	var svc *codegen.ServiceAgentsData
	for _, s := range data.Services {
		if s.Service.Name == "bravo" {
			svc = s
			break
		}
	}
	require.NotNil(t, svc)
	ag := svc.Agents[0]
	tool := ag.UsedToolsets[0].Tools[0]
	require.Contains(t, tool.MethodPayloadTypeRef, "*types.")
	require.Contains(t, tool.MethodResultTypeRef, "*types.")
}
