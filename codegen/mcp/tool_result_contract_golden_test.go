// This file verifies that MCP tool results are specialized from the authored
// Goa result type before generated code runs.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/testutil"
	"goa.design/goa/v3/expr"
)

func TestBuildToolAdaptersClassifiesResultWireShape(t *testing.T) {
	svc, methods := testService("reports", "object", "text", "list", "notify")
	methods["object"].Result = &expr.AttributeExpr{Type: &expr.Object{
		{Name: "summary", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}}
	methods["list"].Result = &expr.AttributeExpr{Type: &expr.Array{
		ElemType: &expr.AttributeExpr{Type: expr.String},
	}}
	methods["notify"].Result = &expr.AttributeExpr{Type: expr.Empty}

	tools, err := newAdapterGenerator(svc, mcpWithTools(methods)).buildToolAdapters()
	require.NoError(t, err)
	require.Len(t, tools, 4)

	require.True(t, tools[0].HasStructuredResult)
	require.NotEmpty(t, tools[0].OutputSchema)
	require.NotEmpty(t, tools[0].ResultSchema)
	require.False(t, tools[0].TextResult)

	require.True(t, tools[1].TextResult)
	require.Empty(t, tools[1].OutputSchema)
	require.NotEmpty(t, tools[1].ResultSchema)

	require.False(t, tools[2].TextResult)
	require.False(t, tools[2].HasStructuredResult)
	require.NotEmpty(t, tools[2].ResultSchema)

	require.False(t, tools[3].HasResult)
	require.Empty(t, tools[3].ResultSchema)
}

func TestMCPToolResultContractGoldens(t *testing.T) {
	codec := func(name string) *MethodCodecData {
		return &MethodCodecData{
			PayloadEncode: "Encode" + name + "Payload",
			PayloadDecode: "Decode" + name + "Payload",
			ResultEncode:  "Encode" + name + "Result",
			ResultDecode:  "Decode" + name + "Result",
		}
	}
	data := &AdapterData{
		CodecPackage: "mcpcodec",
		Register: &RegisterData{
			HelperName:         "ReportsCoreToolset",
			ServiceName:        "reports",
			SuiteName:          "core",
			SuiteQualifiedName: "reports.core",
			Description:        "Report tools",
			Tools: []RegisterTool{
				{
					ID:                  "summarize",
					Title:               "Summarize",
					QualifiedName:       "reports.core.summarize",
					Description:         "Summarize a report",
					HasPayload:          true,
					HasResult:           true,
					HasStructuredResult: true,
					PayloadType:         "*reports.SummarizePayload",
					ResultType:          "*reports.SummarizeResult",
					InputSchema:         `{"type":"object","additionalProperties":false}`,
					ResultSchema:        `{"type":"object","additionalProperties":false}`,
					ExampleArgs:         `{"text":"report"}`,
					Codec:               codec("Summarize"),
				},
				{
					ID:            "title",
					Title:         "Title",
					QualifiedName: "reports.core.title",
					Description:   "Return a title",
					HasResult:     true,
					TextResult:    true,
					PayloadType:   "any",
					ResultType:    "string",
					InputSchema:   noArgumentsSchema,
					ResultSchema:  `{"type":"string"}`,
					ExampleArgs:   `{}`,
					Codec:         codec("Title"),
				},
				{
					ID:            "tags",
					Title:         "Tags",
					QualifiedName: "reports.core.tags",
					Description:   "Return report tags",
					HasResult:     true,
					PayloadType:   "any",
					ResultType:    "[]string",
					InputSchema:   noArgumentsSchema,
					ResultSchema:  `{"type":"array","items":{"type":"string"}}`,
					ExampleArgs:   `{}`,
					Codec:         codec("Tags"),
				},
				{
					ID:            "notify",
					Title:         "Notify",
					QualifiedName: "reports.core.notify",
					Description:   "Send a notification",
					PayloadType:   "any",
					ResultType:    "any",
					InputSchema:   noArgumentsSchema,
					ExampleArgs:   `{}`,
				},
			},
		},
	}

	data.Tools = []*ToolAdapter{
		{
			Name:                "summarize",
			Description:         "Summarize a report",
			ServiceMethodName:   "Summarize",
			HasPayload:          true,
			HasResult:           true,
			InputSchema:         `{"type":"object","additionalProperties":false}`,
			OutputSchema:        `{"type":"object","additionalProperties":false}`,
			HasStructuredResult: true,
			Codec:               codec("Summarize"),
		},
		{
			Name:              "title",
			Description:       "Return a title",
			ServiceMethodName: "Title",
			HasResult:         true,
			InputSchema:       noArgumentsSchema,
			TextResult:        true,
			Codec:             codec("Title"),
		},
		{
			Name:              "tags",
			Description:       "Return report tags",
			ServiceMethodName: "Tags",
			HasResult:         true,
			InputSchema:       noArgumentsSchema,
			Codec:             codec("Tags"),
		},
		{
			Name:              "notify",
			Description:       "Send a notification",
			ServiceMethodName: "Notify",
			InputSchema:       noArgumentsSchema,
		},
	}
	data.ClientCaller = &ClientCallerData{MCPPackage: "mcpreports", Tools: data.Tools}

	testutil.AssertGo(
		t,
		"testdata/golden/tool_results/adapter.go.golden",
		renderTemplateSection(t, "adapter_tools", data),
	)
	testutil.AssertGo(
		t,
		"testdata/golden/tool_results/caller.go.golden",
		renderTemplateSection(t, "mcp_client_caller", data.ClientCaller),
	)
	register := renderTemplateSection(t, "mcp_register", data)
	require.Contains(t, register, "encoded, err := json.Marshal(resp.Content[0])")
	require.NotContains(t, register, "strconv.Quote")
	require.NotContains(t, register, "Service:")
	require.NotContains(t, register, "Toolset:")
	require.Contains(t, register, `Name:        "reports.core"`)
	testutil.AssertGo(t, "testdata/golden/tool_results/register.go.golden", register)
}

// mcpWithTools builds one MCP expression in the method order used by the test.
func mcpWithTools(methods map[string]*expr.MethodExpr) *mcpexpr.MCPExpr {
	return &mcpexpr.MCPExpr{
		Tools: []*mcpexpr.ToolExpr{
			{Name: "object", Method: methods["object"]},
			{Name: "text", Method: methods["text"]},
			{Name: "list", Method: methods["list"]},
			{Name: "notify", Method: methods["notify"]},
		},
	}
}
