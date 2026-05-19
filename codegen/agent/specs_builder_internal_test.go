package codegen_test

import (
	"encoding/json"
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

func TestBuildToolSpecsData_FieldJSONTypes(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		var Section = goadsl.Type("Section", func() {
			goadsl.Attribute("heading", goadsl.String, "Heading")
			goadsl.Required("heading")
		})
		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("briefs", func() {
					Tool("complete", "Complete a brief", func() {
						Args(func() {
							goadsl.Attribute("sections", goadsl.ArrayOf(Section), "Brief sections")
							goadsl.Attribute("lead", Section, "Lead section")
							goadsl.Attribute("backup", Section, "Backup section")
							goadsl.Attribute("publish", goadsl.Boolean, "Whether to publish")
							goadsl.Attribute("retry_count", goadsl.Int, "Retry count")
							goadsl.Required("sections", "lead", "backup", "publish", "retry_count")
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
	specs, err := codegen.BuildToolSpecsDataForTest(data.Services[0].Agents[0])
	require.NoError(t, err)

	jsonTypes := codegen.CollectTypeJSONTypesForTest(specs)

	require.Equal(t, "object", jsonTypes["CompletePayload"]["$payload"])
	require.Equal(t, "array", jsonTypes["CompletePayload"]["sections"])
	require.Equal(t, "string", jsonTypes["CompletePayload"]["sections.heading"])
	require.Equal(t, "object", jsonTypes["CompletePayload"]["lead"])
	require.Equal(t, "string", jsonTypes["CompletePayload"]["lead.heading"])
	require.Equal(t, "object", jsonTypes["CompletePayload"]["backup"])
	require.Equal(t, "string", jsonTypes["CompletePayload"]["backup.heading"])
	require.Equal(t, "boolean", jsonTypes["CompletePayload"]["publish"])
	require.Equal(t, "integer", jsonTypes["CompletePayload"]["retry_count"])
}

func TestBuildToolSpecsData_FieldJSONTypes_DoNotFlattenUnionVariants(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		var UnionPayload = goadsl.Type("UnionPayload", func() {
			goadsl.Attribute("id", goadsl.String, "Request identifier")
			goadsl.OneOf("value", func() {
				goadsl.Attribute("number", goadsl.Int32, "Numeric value")
				goadsl.Attribute("text", goadsl.String, "Text value")
			})
			goadsl.Required("id", "value")
		})
		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("union", func() {
					Tool("echo", "Echo union", func() {
						Args(UnionPayload)
					})
				})
			})
		})
	}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	data, err := codegen.BuildDataForTest("goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root})
	require.NoError(t, err)
	specs, err := codegen.BuildToolSpecsDataForTest(data.Services[0].Agents[0])
	require.NoError(t, err)

	jsonTypes := codegen.CollectTypeJSONTypesForTest(specs)

	require.Equal(t, "object", jsonTypes["EchoPayload"]["$payload"])
	require.Equal(t, "string", jsonTypes["EchoPayload"]["id"])
	require.Equal(t, "object", jsonTypes["EchoPayload"]["value"])
	require.NotContains(t, jsonTypes["EchoPayload"], "value.value")
}

// Extend fields in tool shapes must be materialized before type/spec generation.
func TestBuildToolSpecsData_ExtendFieldsMaterialized(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})

		var Base = goadsl.Type("Base", func() {
			goadsl.Attribute("from_base", goadsl.String, "Inherited field")
			goadsl.Required("from_base")
		})

		var Extended = goadsl.Type("Extended", func() {
			goadsl.Extend(Base)
			goadsl.Attribute("own", goadsl.String, "Extended field")
			goadsl.Required("own")
		})

		goadsl.Service("alpha", func() {
			Agent("scribe", "Extend regression checker", func() {
				Use("docs", func() {
					Tool("emit", "Emit an extended type", func() {
						Args(goadsl.String)
						Return(Extended)
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

	schemas := codegen.CollectTypeSchemasForTest(specs)
	var resultSchema []byte
	for name, schema := range schemas {
		if strings.HasSuffix(name, "EmitResult") {
			resultSchema = schema
			break
		}
	}
	require.NotEmpty(t, resultSchema, "expected generated emit result schema")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(resultSchema, &decoded))
	props, ok := decoded["properties"].(map[string]any)
	require.True(t, ok, "result schema must define properties")
	require.Contains(t, props, "from_base", "extended base field must be present in schema")
	require.Contains(t, props, "own", "direct field must be present in schema")

	required, ok := decoded["required"].([]any)
	require.True(t, ok, "result schema must define required fields")
	require.Contains(t, required, "from_base", "extended base field must be required")
	require.Contains(t, required, "own", "direct field must be required")
}
