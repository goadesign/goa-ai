// Package codegen verifies that generated MCP tool calls return one result directly.
// The tests render the real templates so transport streaming code cannot return.
package codegen

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/testutil"
	gcodegen "goa.design/goa/v3/codegen"
)

func TestGenerateMCPTransport_RendersUnaryToolsCall(t *testing.T) {
	svc, methods := testService("calc", "add")
	mcp := &mcpexpr.MCPExpr{
		Name:    "calc",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "add", Method: methods["add"]},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
	).buildAdapterData()
	require.NoError(t, err)
	data.CodecImportPath = "example.com/calc/gen/mcp_calc/internal/codec"
	data.CodecPackage = testCodecPackage
	data.NeedsServerCodec = true
	data.Tools[0].Codec = &MethodCodecData{ResultEncode: "EncodeAddResult"}

	files := generateMCPTransport("example.com/calc/gen", svc, data)
	require.NotEmpty(t, files)
	rendered := renderGeneratedFile(t, files[0])

	require.Contains(t, rendered, "func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload) (*ToolsCallResult, error)")
	require.Contains(t, rendered, "return final, nil")
	require.Contains(t, rendered, "return toolCallError(a.mapError(err).Error()), nil")
	require.NotContains(t, rendered, "ToolsCallServerStream")
	require.NotContains(t, rendered, "StreamBridge")
	require.NotContains(t, rendered, "SendAndClose")
}

func TestGenerateMCPTransport_RendersMCP202506ToolResults(t *testing.T) {
	rendered := renderTemplateSection(t, "adapter_tools", &AdapterData{
		CodecPackage: "codec",
		Tools: []*ToolAdapter{
			{
				Name:                "summarize",
				Description:         "Summarize one document",
				ServiceMethodName:   "Summarize",
				HasPayload:          true,
				HasResult:           true,
				InputSchema:         `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`,
				OutputSchema:        `{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`,
				HasStructuredResult: true,
				Codec: &MethodCodecData{
					PayloadDecode: "DecodeSummarizePayload",
					ResultEncode:  "EncodeSummarizeResult",
				},
			},
		},
	})

	testutil.AssertGo(t, "testdata/golden/tool_adapter/adapter_tools.go.golden", rendered)
}

func TestClientCaller_RendersUnaryToolsCall(t *testing.T) {
	file := clientCallerFile(&AdapterData{
		mcpPathName: "mcp_calc",
		ClientCaller: &ClientCallerData{
			MCPPackage: "mcppkg",
			Tools:      []*ToolAdapter{{Name: "add"}},
			imports: []*gcodegen.ImportSpec{
				gcodegen.SimpleImport("context"),
				gcodegen.SimpleImport("encoding/json"),
				gcodegen.SimpleImport("errors"),
				gcodegen.SimpleImport("fmt"),
				gcodegen.SimpleImport("sync"),
				gcodegen.NewImport("mcppkg", "example.com/calc/gen/mcp_calc"),
				gcodegen.NewImport("mcpruntime", "goa.design/goa-ai/runtime/mcp"),
			},
		},
	})
	require.NotNil(t, file)
	dir := t.TempDir()
	_, err := file.Render(dir)
	require.NoError(t, err)
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, root.Close())
	})
	content, err := root.ReadFile(file.Path)
	require.NoError(t, err)
	rendered := string(content)

	testutil.AssertGo(t, "testdata/golden/client_caller/caller.go.golden", rendered)
	require.NotContains(t, rendered, ".Recv(ctx)")
	require.NotContains(t, rendered, "ToolsCallClientStream")
}
