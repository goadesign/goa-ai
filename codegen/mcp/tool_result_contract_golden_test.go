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

	tools, err := newAdapterGenerator(testSchemaAPI(), svc, mcpWithTools(methods)).buildToolAdapters()
	require.NoError(t, err)
	require.Len(t, tools, 4)

	byName := make(map[string]*ToolAdapter)
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"object", "text", "list"} {
		require.True(t, byName[name].HasResult)
		require.NotEmpty(t, byName[name].OutputSchema)
		require.NotEmpty(t, byName[name].ResultSchema)
	}
	require.False(t, byName["notify"].HasResult)
	require.Empty(t, byName["notify"].OutputSchema)
}

func TestMCPToolResultContractGoldens(t *testing.T) {
	codec := func(name string) *MethodCodecData {
		return &MethodCodecData{
			PayloadDecode: "Decode" + name + "Payload",
			ResultEncode:  "mcpcodec.Encode" + name + "Result",
		}
	}
	data := &AdapterData{
		PayloadRefs:  testProtocolPayloadRefs(),
		CodecPackage: "mcpcodec",
	}

	data.Tools = []*ToolAdapter{
		{
			Name:         "summarize",
			Description:  "Summarize a report",
			Endpoint:     &endpointMethodAdapter{CallName: "invokeMCPMethod0"},
			HasPayload:   true,
			HasResult:    true,
			InputSchema:  `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`,
			OutputSchema: `{"type":"object","additionalProperties":false}`,
			Codec:        codec("Summarize"),
		},
		{
			Name:        "title",
			Description: "Return a title",
			Endpoint:    &endpointMethodAdapter{CallName: "invokeMCPMethod1"},
			HasResult:   true,
			InputSchema: noArgumentsSchema,
			Codec:       codec("Title"),
		},
		{
			Name:        "tags",
			Description: "Return report tags",
			Endpoint:    &endpointMethodAdapter{CallName: "invokeMCPMethod2"},
			HasResult:   true,
			InputSchema: noArgumentsSchema,
			Codec:       codec("Tags"),
		},
		{
			Name:        "notify",
			Description: "Send a notification",
			Endpoint:    &endpointMethodAdapter{CallName: "invokeMCPMethod3"},
			InputSchema: noArgumentsSchema,
		},
	}
	data.ClientCaller = &ClientCallerData{PayloadRef: "mcpreports.ToolsCallPayload", Tools: data.Tools}

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
