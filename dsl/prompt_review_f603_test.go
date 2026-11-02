// Package dsl_test verifies prompt, tool, and completion DSL behaviors and inherited schema materialization.
package dsl_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/internal/dslshape"
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

// TestReviewA156ReferenceExtend checks that references cannot mutate a merged base.
func TestReviewA156ReferenceExtend(t *testing.T) {
	for _, kind := range []string{"prompt", "tool", "completion", "goa_method"} {
		t.Run(kind, func(t *testing.T) {
			var base, ref goaexpr.UserType
			err := runDSLWithError(t, func() {
				base = Type("Base", func() {
					Attribute("query", String, "Base query", func() { MinLength(2) })
					Required("query")
				})
				ref = Type("ReferenceDefaults", func() {
					Attribute("query", String, "Reference query", func() { Default("ref-default") })
				})
				mixed := func() {
					Reference(ref)
					Extend(base)
					Attribute("query", String, "Consumer query")
				}
				if kind == "tool" {
					Toolset("queries", func() {
						Tool("mixed", "Query", func() { Args(mixed); Return(base) })
					})
					return
				}
				Service("support", func() {
					switch kind {
					case "prompt":
						Prompt("mixed", func() { Template(`{{.Query}}`); Args(mixed) })
						Prompt("plain", func() { Template(`{{.Query}}`); Args(base) })
					case "completion":
						Completion("mixed", "Query", func() { Return(mixed) })
					case "goa_method":
						Method("mixed", func() { Payload(mixed) })
					}
				})
			})
			require.NoError(t, err)
			query := base.Attribute().Find("query")
			t.Logf("base default=%#v description=%q", query.DefaultValue, query.Description)
			assert.Nil(t, query.DefaultValue, "mixing a Reference with Extend must not put its default on the shared Base")
			assert.Equal(t, "Base query", query.Description)
			var args *goaexpr.AttributeExpr
			switch kind {
			case "prompt":
				args = agentsexpr.Root.Prompts[0].Args
				plain := agentsexpr.Root.Prompts[1].Args.Find("query")
				t.Logf("other prompt default=%#v", plain.DefaultValue)
				assert.Nil(t, plain.DefaultValue, "the other caller only supplied Base, so the reference default cannot affect it")
			case "tool":
				args = agentsexpr.Root.Toolsets[0].Tools[0].Args
			case "completion":
				args = agentsexpr.Root.Completions[0].Return
			case "goa_method":
				args = goaexpr.Root.Service("support").Methods[0].Payload
			}
			t.Logf("consumer default=%#v description=%q", args.Find("query").DefaultValue, args.Find("query").Description)
			assert.Nil(t, args.Find("query").DefaultValue, "Goa Merge replaces the consumer field with the base field")
		})
	}
}

// TestReviewA156Preservation keeps inherited details and private customizations stable.
func TestReviewA156Preservation(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "original_first", true: "copy_first"}[reverse], func(t *testing.T) {
			var base goaexpr.UserType
			err := runDSLWithError(t, func() {
				base = Type("Base", func() {
					Attribute("query", String, "Base query", func() {
						MinLength(2)
						Default("ab")
						Example("abc")
						Meta("struct:field:name", "QueryText")
					})
					Attribute("extra", String, "Optional extra")
					Required("query")
				})
				derived := Type("Derived", func() { Extend(base) })
				Service("support", func() {
					original := func() { Prompt("original", func() { Template("ok"); Args(derived) }) }
					custom := func() { Prompt("copy", func() { Template("ok"); Args(derived, func() { Required("extra") }) }) }
					if reverse {
						custom()
						original()
					} else {
						original()
						custom()
					}
				})
			})
			require.NoError(t, err)
			for _, p := range agentsexpr.Root.Prompts {
				for range 3 {
					p.Prepare()
					p.Finalize()
				}
				query := p.Args.Find("query")
				require.NotNil(t, query)
				assert.Equal(t, "Base query", query.Description)
				assert.Equal(t, "ab", query.DefaultValue)
				require.NotNil(t, query.Validation)
				require.NotNil(t, query.Validation.MinLength)
				assert.Equal(t, 2, *query.Validation.MinLength)
				assert.Equal(t, []string{"QueryText"}, query.Meta["struct:field:name"])
				require.Len(t, query.UserExamples, 1)
				assert.Equal(t, "abc", query.UserExamples[0].Value)
				assert.Contains(t, p.Args.AllRequired(), "query")
				assert.Equal(t, p.Name == "copy", p.Args.IsRequired("extra"))
			}
			assert.False(t, base.Attribute().IsRequired("extra"))
		})
	}
}

// TestReviewA156SelfRecursiveShapes preserves inherited fields behind self references.
func TestReviewA156SelfRecursiveShapes(t *testing.T) {
	for _, shape := range []string{"object", "array", "map", "union"} {
		for _, reverse := range []bool{false, true} {
			t.Run(shape+map[bool]string{false: "/recursive_first", true: "/nested_first"}[reverse], func(t *testing.T) {
				err := runDSLWithError(t, func() {
					base := Type("Base", func() {
						Attribute("query", String, "Base query", func() { MinLength(2) })
						Required("query")
					})
					derived := Type("Derived", func() { Extend(base) })
					node := Type("Node", func() {
						self := func() {
							switch shape {
							case "object":
								Attribute("self", "Node", "Optional self")
							case "array":
								Attribute("self", ArrayOf("Node"), "Optional selves")
							case "map":
								Attribute("self", MapOf(String, "Node"), "Optional selves")
							case "union":
								OneOf("self", "Optional self", func() { Attribute("node", "Node", "Node branch") })
							}
						}
						if !reverse {
							self()
						}
						Attribute("nested", &goaexpr.Object{}, "Nested query", func() { Extend(derived) })
						if reverse {
							self()
						}
					})
					Service("support", func() { Prompt("main", func() { Template("ok"); Args(node) }) })
				})
				require.NoError(t, err)
				args := agentsexpr.Root.Prompts[0].Args
				dslshape.Finalize(args)
				nested := args.Find("nested")
				require.NotNil(t, nested)
				query := nested.Find("query")
				require.NotNil(t, query)
				require.NotNil(t, query.Validation)
				require.NotNil(t, query.Validation.MinLength)
				assert.Equal(t, 2, *query.Validation.MinLength)
				assert.True(t, nested.IsRequired("query"))
			})
		}
	}
}

// TestReviewA156DeletionBoundary verifies that dslshape.Finalize finalizes derived bases before merging.
func TestReviewA156DeletionBoundary(t *testing.T) {
	minimum := 2
	base := &goaexpr.UserTypeExpr{
		TypeName: "Base",
		AttributeExpr: &goaexpr.AttributeExpr{
			Type: &goaexpr.Object{
				{Name: "query", Attribute: &goaexpr.AttributeExpr{Type: goaexpr.String, Validation: &goaexpr.ValidationExpr{MinLength: &minimum}}},
			},
			Validation: &goaexpr.ValidationExpr{Required: []string{"query"}},
		},
	}
	derived := &goaexpr.UserTypeExpr{TypeName: "Derived", AttributeExpr: &goaexpr.AttributeExpr{Type: &goaexpr.Object{}, Bases: []goaexpr.DataType{base}}}
	args := &goaexpr.AttributeExpr{Type: &goaexpr.Object{}, Bases: []goaexpr.DataType{derived}}
	dslshape.Finalize(args)
	query := args.Find("query")
	require.NotNil(t, query)
	assert.Contains(t, args.AllRequired(), "query")
	assert.Equal(t, 2, *query.Validation.MinLength)
}

// TestReviewBeaeBaseValueCycle checks that a base's value reference cannot clear an unfinished consumer's inheritance.
func TestReviewBeaeBaseValueCycle(t *testing.T) {
	for _, shape := range []string{"object", "array", "map", "union"} {
		for _, reverse := range []bool{false, true} {
			t.Run(shape+map[bool]string{false: "/recursive_first", true: "/nested_first"}[reverse], func(t *testing.T) {
				err := runDSLWithError(t, func() {
					ancestor := Type("Ancestor", func() {
						Attribute("query", String, "Query", func() { MinLength(2) })
						switch shape {
						case "object":
							Attribute("back", "Node", "Optional back reference")
						case "array":
							Attribute("back", ArrayOf("Node"), "Optional back references")
						case "map":
							Attribute("back", MapOf(String, "Node"), "Optional back references")
						case "union":
							OneOf("back", "Optional back reference", func() { Attribute("node", "Node", "Node branch") })
						}
						Required("query")
					})
					base := Type("Base", func() { Extend(ancestor) })
					derived := Type("Derived", func() { Extend(base) })
					node := Type("Node", func() {
						recursive := func() { Attribute("self", "Node", "Optional self") }
						if !reverse {
							recursive()
						}
						Attribute("nested", &goaexpr.Object{}, "Nested query", func() { Extend(derived) })
						if reverse {
							recursive()
						}
					})
					Service("support", func() { Prompt("main", func() { Template("ok"); Args(node) }) })
				})
				require.NoError(t, err)
				p := agentsexpr.Root.Prompts[0]
				for range 3 {
					p.Prepare()
					p.Finalize()
				}
				nested := p.Args.Find("nested")
				require.NotNil(t, nested)
				query := nested.Find("query")
				require.NotNil(t, query, "Base.Query must reach the consumer even when Base has a value reference back to Node")
				assert.True(t, nested.IsRequired("query"))
				require.NotNil(t, query.Validation)
				require.NotNil(t, query.Validation.MinLength)
				assert.Equal(t, 2, *query.Validation.MinLength)
			})
		}
	}
}

// TestReviewBeaeMultipleSources checks the existing precedence across two references, two bases and private described copies.
func TestReviewBeaeMultipleSources(t *testing.T) {
	for _, kind := range []string{"prompt", "tool_args", "tool_return", "completion", "goa_method"} {
		t.Run(kind, func(t *testing.T) {
			var base1, base2 goaexpr.UserType
			err := runDSLWithError(t, func() {
				base1 = Type("BaseOne", func() {
					Attribute("query", String, "Base first", func() { Default("base-first"); MinLength(2) })
					Required("query")
				})
				base2 = Type("BaseTwo", func() {
					Attribute("query", String, "Base last", func() { Default("base-last"); MinLength(3) })
					Attribute("second", String, "Second field")
					Required("second")
				})
				ref1 := Type("RefOne", func() {
					Attribute("query", String, "Reference first", func() { Default("ref-first") })
					Attribute("reference_only", String, "Reference first", func() { Default("ref-first") })
				})
				ref2 := Type("RefTwo", func() {
					Attribute("query", String, "Reference last", func() { Default("ref-last") })
					Attribute("reference_only", String, "Reference last", func() { Default("ref-last") })
				})
				mixed := func() {
					Reference(ref1)
					Reference(ref2)
					Extend(base1)
					Extend(base2)
					Attribute("query", String, "Consumer query")
					Attribute("reference_only", String, "Reference value")
				}
				if kind == "tool_args" || kind == "tool_return" {
					Toolset("queries", func() {
						Tool("mixed", "Query", func() {
							if kind == "tool_args" {
								Args(mixed)
								Return(base1)
							} else {
								Args(base1)
								Return(mixed)
							}
						})
					})
					return
				}
				Service("support", func() {
					switch kind {
					case "prompt":
						Prompt("main", func() { Template("ok"); Args(mixed) })
						Prompt("described", func() { Template("ok"); Args(base1, "Private description") })
						Prompt("plain", func() { Template("ok"); Args(base1) })
					case "completion":
						Completion("mixed", "Query", func() { Return(mixed) })
					case "goa_method":
						Method("mixed", func() { Payload(mixed) })
					}
				})
			})
			require.NoError(t, err)
			var att *goaexpr.AttributeExpr
			switch kind {
			case "prompt":
				for _, p := range agentsexpr.Root.Prompts {
					for range 3 {
						p.Prepare()
						p.Finalize()
					}
				}
				att = agentsexpr.Root.Prompts[0].Args
				described := agentsexpr.Root.Prompts[1].Args
				plain := agentsexpr.Root.Prompts[2].Args
				assert.Equal(t, described.Type.(goaexpr.UserType).ID(), plain.Type.(goaexpr.UserType).ID())
				assert.NotSame(t, described.Type, plain.Type)
				assert.Equal(t, "Private description", described.Description)
				assert.Equal(t, "base-first", plain.Find("query").DefaultValue)
			case "tool_args", "tool_return":
				tool := agentsexpr.Root.Toolsets[0].Tools[0]
				for range 3 {
					tool.Finalize()
				}
				att = tool.Args
				if kind == "tool_return" {
					att = tool.Return
				}
			case "completion":
				c := agentsexpr.Root.Completions[0]
				for range 3 {
					c.Finalize()
				}
				att = c.Return
			case "goa_method":
				att = goaexpr.Root.Service("support").Methods[0].Payload
			}
			assert.Equal(t, "base-last", att.Find("query").DefaultValue)
			assert.Equal(t, "ref-first", att.Find("reference_only").DefaultValue)
			assert.Equal(t, "Base last", att.Find("query").Description)
			require.NotNil(t, att.Find("query").Validation.MinLength)
			assert.Equal(t, 3, *att.Find("query").Validation.MinLength)
			assert.ElementsMatch(t, []string{"query", "second"}, att.AllRequired())
			assert.Equal(t, "base-first", base1.Attribute().Find("query").DefaultValue)
			assert.Equal(t, "base-last", base2.Attribute().Find("query").DefaultValue)
		})
	}
}

// TestReviewBeaeNestedReferenceSharedBase verifies that when a Reference target
// itself uses Reference + Extend on a shared base and eval.RunDSL() runs full root
// finalization, the shared base default remains nil.
func TestReviewBeaeNestedReferenceSharedBase(t *testing.T) {
	var sharedBase goaexpr.UserType
	err := runDSLWithError(t, func() {
		sharedBase = Type("SharedBase", func() {
			Attribute("query", String, "Shared base query", func() { MinLength(2) })
			Required("query")
		})
		refInner := Type("RefInner", func() {
			Attribute("query", String, "Inner default", func() { Default("inner-default") })
		})
		refOuter := Type("RefOuter", func() {
			Reference(refInner)
			Extend(sharedBase)
			Attribute("query", String, "Outer query")
		})
		Service("support", func() {
			Prompt("main", func() {
				Template("ok")
				Args(func() {
					Reference(refOuter)
					Extend(sharedBase)
					Attribute("query", String, "Main query")
				})
			})
		})
	})
	require.NoError(t, err)
	query := sharedBase.Attribute().Find("query")
	require.NotNil(t, query)
	assert.Nil(t, query.DefaultValue, "SharedBase default must remain nil after full root finalization")
}

// TestIndependentBA23BackEdge checks value cycles that cross inheritance dependencies.
func TestIndependentBA23BackEdge(t *testing.T) {
	for _, kind := range []string{"prompt", "tool", "completion", "goa"} {
		for _, shape := range []string{"object", "array", "map", "union"} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/reverse_%v", kind, shape, reverse), func(t *testing.T) {
					var node goaexpr.UserType
					err := runDSLWithError(t, func() {
						ancestor := Type("Ancestor", func() {
							Attribute("query", String, "Query", func() { MinLength(2) })
							Required("query")
							switch shape {
							case "object":
								Attribute("back", "Node", "Back to node")
							case "array":
								Attribute("back", ArrayOf("Node"), "Back to nodes")
							case "map":
								Attribute("back", MapOf(String, "Node"), "Back to nodes")
							case "union":
								OneOf("back", "Back to node", func() { Attribute("node", "Node", "Node branch") })
							}
						})
						base := Type("Base", func() { Extend(ancestor) })
						derived := Type("Derived", func() { Extend(base) })
						node = Type("Node", func() {
							if !reverse {
								Attribute("self", "Node", "Self")
							}
							Attribute("nested", &goaexpr.Object{}, "Nested", func() { Extend(derived) })
							if reverse {
								Attribute("self", "Node", "Self")
							}
						})
						if kind == "tool" {
							Toolset("queries", func() { Tool("query", "Query", func() { Args(node); Return(node) }) })
							return
						}
						Service("support", func() {
							switch kind {
							case "prompt":
								Prompt("main", func() { Template("ok"); Args(node) })
							case "completion":
								Completion("query", "Query", func() { Return(node) })
							case "goa":
								Method("query", func() { Payload(node); Result(node) })
							}
						})
					})
					require.NoError(t, err)
					atts := []*goaexpr.AttributeExpr{node.Attribute()}
					if kind == "prompt" {
						atts = append(atts, agentsexpr.Root.Prompts[0].Args)
					}
					for _, att := range atts {
						nested := att.Find("nested")
						require.NotNil(t, nested)
						query := nested.Find("query")
						require.NotNil(t, query, "nested query must survive")
						assert.Contains(t, nested.AllRequired(), "query")
						require.NotNil(t, query.Validation)
						require.NotNil(t, query.Validation.MinLength)
						assert.Equal(t, 2, *query.Validation.MinLength)
					}
				})
			}
		}
	}
}
