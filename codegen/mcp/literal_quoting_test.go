// This file verifies that static DSL text remains valid when MCP adapters turn
// it into Go source.
package codegen

import (
	"go/format"
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/testutil"
	"goa.design/goa/v3/expr"
)

func TestGeneratedAdapterQuotesStaticDSLText(t *testing.T) {
	svc, methods := testService("assistant", "summarize")
	methods["summarize"].Payload = &expr.AttributeExpr{Type: &expr.Object{
		{
			Name: "text",
			Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Text written with `inline code`.",
			},
		},
	}}
	methods["summarize"].Result = &expr.AttributeExpr{Type: &expr.Object{
		{
			Name: "summary",
			Attribute: &expr.AttributeExpr{
				Type:        expr.String,
				Description: "Summary written with `inline code`.",
			},
		},
	}}
	mcp := &mcpexpr.MCPExpr{
		Tools: []*mcpexpr.ToolExpr{
			{
				Name:        "summarize",
				Description: "Summarize one document.",
				Method:      methods["summarize"],
			},
		},
		Prompts: []*mcpexpr.PromptExpr{
			{
				Name:        "daily \"summary\"\nreport",
				Description: "Prepare the daily report.",
				Messages: []*mcpexpr.MessageExpr{
					{
						Role:    "user",
						Content: "Summarize today.",
					},
				},
			},
		},
	}
	generator := newAdapterGenerator(svc, mcp)
	tools, err := generator.buildToolAdapters()
	require.NoError(t, err)
	require.Len(t, tools, 1)
	tools[0].ServiceMethodName = "Summarize"
	tools[0].Codec = &MethodCodecData{
		PayloadDecode: "DecodeSummarizePayload",
		ResultEncode:  "EncodeSummarizeResult",
	}
	data := &AdapterData{
		CodecPackage:  "codec",
		Tools:         tools,
		StaticPrompts: generator.buildStaticPrompts(),
	}

	generated := "package generated\n\n" +
		renderTemplateSection(t, "adapter_tools", data) +
		renderTemplateSection(t, "adapter_prompts", data)
	formatted, err := format.Source([]byte(generated))
	require.NoError(t, err, "generated MCP adapter must be valid Go")

	testutil.AssertGo(t, "testdata/golden/literal_quoting/adapter.go.golden", string(formatted))
}
