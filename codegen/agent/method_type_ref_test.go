package codegen_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
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
				Use("lookup", func() {
					Tool("by_id", "Lookup by ID", func() {
						Args(IDT)
						Return(OkT)
						BindTo("Find")
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
				Use("docs", func() {
					Tool("store", "Store", func() {
						Args(Doc)
						Return(Doc)
						BindTo("Store")
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

// TestBuildFilesDeclaredErrorsCodegen verifies that generated service_executor.go
// and provider.go specialize eligible declared errors into inline FailureKind
// switches while omitting Fault() and Timeout() design errors.
func TestBuildFilesDeclaredErrorsCodegen(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("orders", func() {})
		var OrderPayload = goadsl.Type("OrderPayload", func() {
			goadsl.Attribute("id", goadsl.String, "Order ID")
			goadsl.Required("id")
		})
		var OrderResult = goadsl.Type("OrderResult", func() {
			goadsl.Attribute("ok", goadsl.Boolean, "Success")
			goadsl.Required("ok")
		})
		goadsl.Service("orders", func() {
			goadsl.Error("invalid_state", goadsl.ErrorResult, "Invalid order state")
			goadsl.Error("temp_busy", goadsl.ErrorResult, "Backend busy", func() {
				goadsl.Temporary()
			})
			goadsl.Error("internal_crash", goadsl.ErrorResult, "Server fault", func() {
				goadsl.Fault()
			})
			goadsl.Error("slow_upstream", goadsl.ErrorResult, "Upstream timeout", func() {
				goadsl.Timeout()
			})
			goadsl.Method("UpdateOrder", func() {
				goadsl.Payload(OrderPayload)
				goadsl.Result(OrderResult)
				goadsl.Error("invalid_state")
				goadsl.Error("temp_busy")
				goadsl.Error("internal_crash")
				goadsl.Error("slow_upstream")
			})
			Agent("clerk", "Order clerk", func() {
				Use("manage", func() {
					Tool("update_order", "Update an order", func() {
						Args(OrderPayload)
						Return(OrderResult)
						BindTo("UpdateOrder")
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.BuildFilesForTest("example.com/orders", []eval.Root{goaexpr.Root, agentsExpr.Root}, false)
	require.NoError(t, err)

	renderFile := func(target string) string {
		for _, f := range files {
			if filepath.ToSlash(f.Path) == filepath.ToSlash(target) {
				var buf bytes.Buffer
				for _, s := range f.SectionTemplates {
					require.NoError(t, s.Write(&buf))
				}
				return buf.String()
			}
		}
		return ""
	}

	serviceExec := renderFile("gen/orders/agents/clerk/manage/service_executor.go")
	require.NotEmpty(t, serviceExec)
	assert.Contains(t, serviceExec, `case "invalid_state":`)
	assert.Contains(t, serviceExec, `return planner.FailureDomainRejection, true`)
	assert.Contains(t, serviceExec, `case "temp_busy":`)
	assert.Contains(t, serviceExec, `return planner.FailureUnavailable, true`)
	assert.NotContains(t, serviceExec, `"internal_crash"`)
	assert.NotContains(t, serviceExec, `"slow_upstream"`)
	assert.Contains(t, serviceExec, `toolregistry.EligibleDeclaredToolFailure(err, declared[0])`)

	provider := renderFile("gen/orders/toolsets/manage/provider.go")
	require.NotEmpty(t, provider)
	assert.Contains(t, provider, `toolregistry.NewToolResultDeclaredServiceErrorMessage(`)
	assert.Contains(t, provider, `case "invalid_state":`)
	assert.Contains(t, provider, `return planner.FailureDomainRejection, true`)
	assert.Contains(t, provider, `case "temp_busy":`)
	assert.Contains(t, provider, `return planner.FailureUnavailable, true`)
	assert.NotContains(t, provider, `"internal_crash"`)
	assert.NotContains(t, provider, `"slow_upstream"`)
}
