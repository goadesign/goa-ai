// Package dsl_test verifies prompt, tool, and completion DSL behaviors and inherited schema materialization.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
	goaexpr "goa.design/goa/v3/expr"
)

// TestReviewF603RecursiveExtend checks that recursive named types do not finalize
// an unvisited sibling before its inherited dependencies have been prepared.
func TestReviewF603RecursiveExtend(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "recursive_first", true: "nested_first"}[reverse], func(t *testing.T) {
			err := runDSLWithError(t, func() {
				base := Type("Base", func() {
					Attribute("query", String, "Query", func() { MinLength(2) })
					Required("query")
				})
				derived := Type("Derived", func() { Extend(base) })
				node := Type("Node", func() {
					if !reverse {
						Attribute("other", "Other", "Other node")
					}
					Attribute("nested", &goaexpr.Object{}, "Nested query", func() { Extend(derived) })
					Required("nested")
					if reverse {
						Attribute("other", "Other", "Other node")
					}
				})
				Type("Other", func() { Attribute("node", node, "Node") })
				Service("support", func() {
					Prompt("main", func() {
						Template(`{{.Root.Nested.Query}}`)
						Args(func() {
							Attribute("root", node, "Root")
							Required("root")
						})
					})
				})
			})
			require.NoError(t, err)
		})
	}
}

// TestReviewF603RecursiveShapes checks the same dependency order through each
// supported nested shape, preserving positive controls with the sibling moved.
func TestReviewF603RecursiveShapes(t *testing.T) {
	for _, shape := range []string{"object", "array", "map", "union", "reference"} {
		for _, reverse := range []bool{false, true} {
			t.Run(shape+map[bool]string{false: "/recursive_first", true: "/nested_first"}[reverse], func(t *testing.T) {
				err := runDSLWithError(t, func() {
					base := Type("Base", func() {
						Attribute("query", String, "Query", func() { MinLength(2) })
						Required("query")
					})
					derived := Type("Derived", func() { Extend(base) })
					node := Type("Node", func() {
						if !reverse {
							Attribute("other", "Other", "Other node")
						}
						switch shape {
						case "object":
							Attribute("nested", &goaexpr.Object{}, "Nested query", func() { Extend(derived) })
						case "array":
							Attribute("nested", ArrayOf(&goaexpr.Object{}, func() { Extend(derived) }), "Nested queries")
						case "map":
							Attribute("nested", MapOf(String, &goaexpr.Object{}, func() { Elem(func() { Extend(derived) }) }), "Nested queries")
						case "union":
							OneOf("nested", "Nested query", func() {
								Attribute("structured", &goaexpr.Object{}, "Query", func() { Extend(derived) })
							})
						case "reference":
							Attribute("nested", &goaexpr.Object{}, "Nested query", func() {
								Reference(derived)
								Attribute("query")
							})
						}
						if reverse {
							Attribute("other", "Other", "Other node")
						}
					})
					Type("Other", func() { Attribute("node", node, "Node") })
					Service("support", func() {
						Prompt("main", func() {
							Template("ok")
							Args(node)
						})
					})
				})
				require.NoError(t, err)
				nested := agentsexpr.Root.Prompts[0].Args.Find("nested")
				require.NotNil(t, nested)
				switch shape {
				case "array":
					nested = goaexpr.AsArray(nested.Type).ElemType
				case "map":
					nested = goaexpr.AsMap(nested.Type).ElemType
				case "union":
					nested = goaexpr.AsUnion(nested.Type).Values[0].Attribute
				}
				query := nested.Find("query")
				require.NotNil(t, query, "inherited query must survive")
				require.NotNil(t, query.Validation, "inherited validation must survive")
				require.NotNil(t, query.Validation.MinLength)
				require.Equal(t, 2, *query.Validation.MinLength)
				if shape != "reference" {
					require.Contains(t, nested.AllRequired(), "query")
				}
			})
		}
	}
}

// TestReviewF603SharedCallers preserves nested inheritance for direct tool
// arguments/results and service-owned completion results without prompt setup.
func TestReviewF603SharedCallers(t *testing.T) {
	for _, kind := range []string{"tool", "completion"} {
		t.Run(kind, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				base := Type("Base", func() {
					Attribute("query", String, "Query", func() { MinLength(2) })
					Required("query")
				})
				derived := Type("Derived", func() { Extend(base) })
				shape := func() {
					Attribute("nested", &goaexpr.Object{}, "Nested query", func() { Extend(derived) })
					Required("nested")
				}
				if kind == "tool" {
					Toolset("queries", func() {
						Tool("query", "Query", func() {
							Args(shape)
							Return(shape)
						})
					})
				} else {
					Service("support", func() {
						Completion("query", "Query", func() {
							Return(shape)
						})
					})
				}
			})
			require.NoError(t, err)
			var atts []*goaexpr.AttributeExpr
			if kind == "tool" {
				tool := agentsexpr.Root.Toolsets[0].Tools[0]
				atts = []*goaexpr.AttributeExpr{tool.Args, tool.Return}
			} else {
				atts = []*goaexpr.AttributeExpr{agentsexpr.Root.Completions[0].Return}
			}
			for _, att := range atts {
				nested := att.Find("nested")
				require.NotNil(t, nested)
				query := nested.Find("query")
				require.NotNil(t, query)
				require.Contains(t, nested.AllRequired(), "query")
				require.NotNil(t, query.Validation)
				require.NotNil(t, query.Validation.MinLength)
				require.Equal(t, 2, *query.Validation.MinLength)
			}
		})
	}
}
