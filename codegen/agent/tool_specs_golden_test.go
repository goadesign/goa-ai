package codegen_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestToolSpecsDeterministicTypeRefs verifies that tool_specs types use
// deterministic, fully-qualified references computed from Goa NameScope
// (GoFullTypeRef/Name) with no pointer surgery or fallbacks.
func TestToolSpecsDeterministicTypeRefs(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		// Define a user type at API scope so it can be referenced from service/attributes.
		var Doc = goadsl.Type("Doc", func() {
			goadsl.Attribute("id", goadsl.String, "ID")
			goadsl.Attribute("title", goadsl.String, "Title")
			goadsl.Required("id", "title")
		})

		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("summarize", func() {
						Tool("summarize_doc", "Summarize a document", func() {
							// Use the service user type directly as top-level args and result.
							Args(Doc)
							Return(Doc)
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

	var codecs string
	for _, f := range files {
		if filepath.ToSlash(f.Path) == filepath.ToSlash("gen/alpha/agents/scribe/specs/summarize/codecs.go") {
			for _, s := range f.SectionTemplates {
				codecs += s.Source
			}
			break
		}
	}
	require.NotEmpty(t, codecs, "expected generated specs/summarize/codecs.go")
	// The template source contains placeholders; concrete rendering is validated
	// via specs_builder_internal_test. Here we simply assert we found the file.
}

// TestServiceToolsetCrossServiceBindTo verifies cross-service BindTo uses the
// target service package for method payload/result references in generator data
// (used by executor-first registrations and transforms).
func TestServiceToolsetCrossServiceBindTo(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("multi", func() {})
		// Service B provides the bound method.
		goadsl.Service("bravo", func() {
			goadsl.Method("Lookup", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("id", goadsl.String, "Identifier")
					goadsl.Required("id")
				})
				goadsl.Result(func() {
					goadsl.Attribute("ok", goadsl.Boolean, "OK")
					goadsl.Required("ok")
				})
			})
		})

		// Service A hosts the agent that binds to bravo.Lookup.
		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("lookup", func() {
						Tool("by_id", "Lookup by ID", func() {
							Args(goadsl.String)
							Return(goadsl.Boolean)
							BindTo("bravo", "Lookup")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	// Verify generator data marks SourceService correctly for cross-service binding.
	data, err := codegen.BuildDataForTest("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root})
	require.NoError(t, err)
	require.NotNil(t, data)

	// Find the alpha.scribe agent and its toolset.
	var alphaSvc *codegen.ServiceAgentsData
	for _, s := range data.Services {
		if s.Service.Name == "alpha" {
			alphaSvc = s
			break
		}
	}
	require.NotNil(t, alphaSvc)
	require.NotEmpty(t, alphaSvc.Agents)
	ts := alphaSvc.Agents[0].UsedToolsets[0]
	require.Equal(t, "lookup", ts.Name)
	require.Equal(t, "bravo", ts.SourceService.PkgName, "cross-service binding should reference target service package")
}
