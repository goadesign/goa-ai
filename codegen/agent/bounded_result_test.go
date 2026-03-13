package codegen_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestBoundedResultProjectsBoundsIntoResultSchemaWithoutMutatingResultType
// verifies that bounded tools keep authored result Go types semantic while the
// generated model-facing JSON schema gains the canonical bounded fields.
func TestBoundedResultProjectsBoundsIntoResultSchemaWithoutMutatingResultType(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_test", func() {})
		goadsl.Service("svc", func() {
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Attribute("cursor", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
						})
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/bounded_result", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var toolTypes string
	expectedPath := filepath.ToSlash("gen/svc/toolsets/tools/types.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			toolTypes = buf.String()
			break
		}
	}
	require.NotEmpty(t, toolTypes, "expected generated types.go at %s", expectedPath)
	require.NotContains(t, toolTypes, "Returned int")
	require.NotContains(t, toolTypes, "Truncated bool")

	var schemas string
	schemasPath := filepath.ToSlash("gen/svc/agents/agent/specs/tool_schemas.json")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == schemasPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			schemas = buf.String()
			break
		}
	}
	require.NotEmpty(t, schemas, "expected generated tool_schemas.json at %s", schemasPath)

	var doc struct {
		Tools []struct {
			ID     string `json:"id"`
			Result struct {
				Schema struct {
					Properties map[string]any `json:"properties"`
					Required   []string       `json:"required"`
				} `json:"schema"`
			} `json:"result"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemas), &doc))
	require.Len(t, doc.Tools, 1)

	props := doc.Tools[0].Result.Schema.Properties
	require.Contains(t, props, "results")
	require.Contains(t, props, "returned")
	require.Contains(t, props, "truncated")
	require.Contains(t, props, "total")
	require.Contains(t, props, "refinement_hint")
	require.Contains(t, props, "next_cursor")
	require.Contains(t, doc.Tools[0].Result.Schema.Required, "returned")
	require.Contains(t, doc.Tools[0].Result.Schema.Required, "truncated")
}

// TestBoundedResultGeneratesBoundsSpec verifies that the generated tool specs
// surface boundedness as a first-class out-of-band contract.
func TestBoundedResultGeneratesBoundsSpec(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_spec_test", func() {})
		goadsl.Service("svc", func() {
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Attribute("cursor", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
						})
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := codegen.Generate("example.com/bounded_result", []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)

	var specs string
	expectedPath := filepath.ToSlash("gen/svc/toolsets/tools/specs.go")
	for _, f := range files {
		if filepath.ToSlash(f.Path) == expectedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			specs = buf.String()
			break
		}
	}
	require.NotEmpty(t, specs, "expected generated specs.go at %s", expectedPath)
	require.Contains(t, specs, "Bounds: &tools.BoundsSpec{")
	require.Contains(t, specs, "Paging: &tools.PagingSpec{")
	require.NotContains(t, specs, "BoundedResult: true")
}

// TestBoundedResultRequiresRequiredReturnedAndTruncated verifies that bounded
// method-backed tools fail DSL validation when required canonical bounds fields
// are present but optional on the bound method result.
func TestBoundedResultRequiresRequiredReturnedAndTruncated(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_required_fields_test", func() {})
		goadsl.Service("svc", func() {
			goadsl.Method("Search", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("query", goadsl.String)
					goadsl.Required("query")
				})
				goadsl.Result(func() {
					goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
					goadsl.Attribute("returned", goadsl.Int)
					goadsl.Attribute("truncated", goadsl.Boolean)
					goadsl.Required("results")
				})
			})
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
						})
						BindTo("svc", "Search")
						BoundedResult()
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	err := eval.RunDSL()
	require.Error(t, err)
	require.ErrorContains(t, err, `bounded method result field "returned" must be required`)
	require.ErrorContains(t, err, `bounded method result field "truncated" must be required`)
}

// TestBoundedResultRequiresMethodNextCursor verifies that cursor-paged bounded
// tools fail DSL validation when the bound method result omits the configured
// next-cursor field.
func TestBoundedResultRequiresMethodNextCursor(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_next_cursor_test", func() {})
		goadsl.Service("svc", func() {
			goadsl.Method("Search", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("query", goadsl.String)
					goadsl.Attribute("cursor", goadsl.String)
					goadsl.Required("query")
				})
				goadsl.Result(func() {
					goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
					goadsl.Attribute("returned", goadsl.Int)
					goadsl.Attribute("truncated", goadsl.Boolean)
					goadsl.Required("results", "returned", "truncated")
				})
			})
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Attribute("cursor", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
						})
						BindTo("svc", "Search")
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	err := eval.RunDSL()
	require.Error(t, err)
	require.ErrorContains(t, err, `bounded method result must define "next_cursor" on the bound method result`)
}

// TestBoundedResultRequiresOptionalMethodNextCursor verifies that cursor-paged
// bounded tools reject bound method results that make the next cursor required.
func TestBoundedResultRequiresOptionalMethodNextCursor(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_optional_next_cursor_test", func() {})
		goadsl.Service("svc", func() {
			goadsl.Method("Search", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("query", goadsl.String)
					goadsl.Attribute("cursor", goadsl.String)
					goadsl.Required("query")
				})
				goadsl.Result(func() {
					goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
					goadsl.Attribute("returned", goadsl.Int)
					goadsl.Attribute("truncated", goadsl.Boolean)
					goadsl.Attribute("next_cursor", goadsl.String)
					goadsl.Required("results", "returned", "truncated", "next_cursor")
				})
			})
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Attribute("cursor", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
						})
						BindTo("svc", "Search")
						BoundedResult(func() {
							Cursor("cursor")
							NextCursor("next_cursor")
						})
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	err := eval.RunDSL()
	require.Error(t, err)
	require.ErrorContains(t, err, `bounded method result field "next_cursor" must be optional`)
}

// TestBoundedResultRequiresOptionalMethodTotalAndRefinementHint verifies that
// bounded tools reject bound method results that make optional canonical bounds
// fields required.
func TestBoundedResultRequiresOptionalMethodTotalAndRefinementHint(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_optional_method_fields_test", func() {})
		goadsl.Service("svc", func() {
			goadsl.Method("Search", func() {
				goadsl.Payload(func() {
					goadsl.Attribute("query", goadsl.String)
					goadsl.Required("query")
				})
				goadsl.Result(func() {
					goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
					goadsl.Attribute("returned", goadsl.Int)
					goadsl.Attribute("truncated", goadsl.Boolean)
					goadsl.Attribute("total", goadsl.Int)
					goadsl.Attribute("refinement_hint", goadsl.String)
					goadsl.Required("results", "returned", "truncated", "total", "refinement_hint")
				})
			})
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
						})
						BindTo("svc", "Search")
						BoundedResult()
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	err := eval.RunDSL()
	require.Error(t, err)
	require.ErrorContains(t, err, `bounded method result field "total" must be optional`)
	require.ErrorContains(t, err, `bounded method result field "refinement_hint" must be optional`)
}

// TestBoundedResultRejectsCanonicalToolReturnFields verifies that explicit
// tool-facing result shapes stay semantic and do not redefine canonical bounded
// fields owned by BoundedResult().
func TestBoundedResultRejectsCanonicalToolReturnFields(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("bounded_result_tool_return_shape_test", func() {})
		goadsl.Service("svc", func() {
			Agent("agent", "desc", func() {
				Use("tools", func() {
					Tool("search", "Search", func() {
						Args(func() {
							goadsl.Attribute("query", goadsl.String)
							goadsl.Required("query")
						})
						Return(func() {
							goadsl.Attribute("results", goadsl.ArrayOf(goadsl.String))
							goadsl.Attribute("total", goadsl.Int)
							goadsl.Required("results", "total")
						})
						BoundedResult()
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	err := eval.RunDSL()
	require.Error(t, err)
	require.ErrorContains(t, err, `bounded tool return must not define canonical bounds field "total"`)
}
