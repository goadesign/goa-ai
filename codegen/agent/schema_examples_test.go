package codegen_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	. "goa.design/goa-ai/dsl"
	agentsExpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// TestToolSchemasContainOnlyAuthoredRootExamples verifies that placeholder
// examples synthesized by Goa never become model-visible tool guidance.
func TestToolSchemasContainOnlyAuthoredRootExamples(t *testing.T) {
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))

	design := func() {
		goadsl.API("alpha", func() {})
		var option = goadsl.Type("Option", func() {
			goadsl.Attribute("id", goadsl.String, "Stable equipment identifier")
			goadsl.Attribute("label", goadsl.String, "Equipment label")
			goadsl.Required("id", "label")
			goadsl.Example(map[string]any{
				"id":    "compressor_1",
				"label": "Compressor 1",
			})
		})
		var payload = goadsl.Type("Payload", func() {
			goadsl.Attribute("query", goadsl.String, "Question to answer")
			goadsl.Attribute("options", goadsl.ArrayOf(option), "Answer options")
			goadsl.Required("query", "options")
			goadsl.Example(map[string]any{
				"query": "Which compressor needs attention?",
				"options": []map[string]any{{
					"id":    "compressor_1",
					"label": "Compressor 1",
				}},
			})
		})
		goadsl.Service("alpha", func() {
			Agent("assistant", "Facility assistant", func() {
				Use("answer", func() {
					Tool("answer_question", "Answer one facility question", func() {
						Args(payload)
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

	schemas := codegen.CollectTypeSchemasForTest(specs)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(schemas["AnswerQuestionPayload"], &schema))
	require.Equal(t, map[string]any{
		"query": "Which compressor needs attention?",
		"options": []any{map[string]any{
			"id":    "compressor_1",
			"label": "Compressor 1",
		}},
	}, schema["example"])
	delete(schema, "example")
	require.Equal(t, 1, countSchemaExamples(schema))
	definitions := schema["$defs"].(map[string]any)
	optionSchema := definitions["Option"].(map[string]any)
	require.Equal(t, map[string]any{
		"id":    "compressor_1",
		"label": "Compressor 1",
	}, optionSchema["example"])
}

// countSchemaExamples counts example annotations in a decoded schema graph.
func countSchemaExamples(node any) int {
	count := 0
	switch actual := node.(type) {
	case map[string]any:
		if _, ok := actual["example"]; ok {
			count++
		}
		for _, child := range actual {
			count += countSchemaExamples(child)
		}
	case []any:
		for _, child := range actual {
			count += countSchemaExamples(child)
		}
	}
	return count
}
