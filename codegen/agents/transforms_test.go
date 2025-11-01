package codegen_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agents"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestTransformsEmitted verifies that transforms.go is emitted when tool payload/result
// are compatible with bound method payload/result types.
func TestTransformsEmitted(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("svc", func() {})
		// Define user types at API scope matching method shapes
		var QPayload = goadsl.Type("QPayload", func() { goadsl.Attribute("q", goadsl.String, "Q"); goadsl.Required("q") })
		var OkResult = goadsl.Type("OkResult", func() { goadsl.Attribute("ok", goadsl.Boolean, "OK") })
		goadsl.Service("svc", func() {
			goadsl.Method("Do", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("q", goadsl.String, "Q")
					goadsl.Required("q")
				})
				goadsl.Result(func() {
					goadsl.Attribute("ok", goadsl.Boolean, "OK")
				})
			})
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("lookup", func() {
						Tool("by_id", "Lookup by ID", func() {
							Args(QPayload)
							Return(OkResult)
							BindTo("Do")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	// Find transforms.go under specs/<toolset>/
	var transforms string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == filepath.ToSlash("gen/svc/agents/scribe/specs/lookup/transforms.go") {
			transforms = f.Path
			break
		}
	}
	require.NotEmpty(t, transforms, "expected transforms.go to be generated")
}

// TestTransformsNotEmittedForIncompatible ensures transforms.go is NOT emitted
// when both payload and result shapes are incompatible between tool and method.
func TestTransformsNotEmittedForIncompatible(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("svc", func() {})
		// Define user types intentionally incompatible with method shapes
		var QPayload = goadsl.Type("QPayload", func() { goadsl.Attribute("q", goadsl.String, "Q") })
		var OkResult = goadsl.Type("OkResult", func() { goadsl.Attribute("ok", goadsl.Boolean, "OK") })
		goadsl.Service("svc", func() {
			goadsl.Method("Do", func() {
				goadsl.Payload(func() { goadsl.Attribute("x", goadsl.Int, "X") })
				goadsl.Result(func() { goadsl.Attribute("y", goadsl.Int, "Y") })
			})
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("lookup", func() {
						Tool("by_id", "Lookup by ID", func() {
							// Not method-backed; incompatible shapes shouldn't matter and no transforms are emitted.
							Args(QPayload)
							Return(OkResult)
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	for _, f := range files {
		if filepath.ToSlash(f.Path) == filepath.ToSlash("gen/svc/agents/scribe/specs/lookup/transforms.go") {
			t.Fatalf("did not expect transforms.go to be generated for incompatible shapes")
		}
	}
}
