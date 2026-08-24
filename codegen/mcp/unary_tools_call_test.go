// Package codegen verifies that generated MCP tool calls return one result directly.
// The tests render the real templates so transport streaming code cannot return.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
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
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
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
	require.NotContains(t, rendered, "ToolsCallServerStream")
	require.NotContains(t, rendered, "StreamBridge")
	require.NotContains(t, rendered, "SendAndClose")
}

func TestClientCaller_RendersUnaryToolsCall(t *testing.T) {
	file := clientCallerFile(&AdapterData{
		mcpPathName: "mcp_calc",
		ClientCaller: &ClientCallerData{
			MCPPackage:     "mcppkg",
			JSONRPCPackage: "genjsonrpc",
		},
	})
	require.NotNil(t, file)
	rendered := renderGeneratedFile(t, file)

	require.Contains(t, rendered, "ires, err := c.client.ToolsCall()(ctx, payload)")
	require.Contains(t, rendered, "result := ires.(*mcppkg.ToolsCallResult)")
	require.NotContains(t, rendered, ".Recv(ctx)")
	require.NotContains(t, rendered, "ToolsCallClientStream")
}

func TestGenerateMCPClientAdapter_RendersUnaryToolsCall(t *testing.T) {
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
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()
	require.NoError(t, err)
	data.CodecImportPath = "example.com/calc/gen/mcp_calc/internal/codec"
	data.CodecPackage = testCodecPackage
	data.NeedsClientCodec = true
	data.Tools[0].ServiceMethodName = "Add"
	data.Tools[0].Codec = &MethodCodecData{ResultDecode: "DecodeAddResult"}
	data.clientMethodNames = []string{"Add"}
	setTestClientRenderNames(data, svc)

	files := generateMCPClientAdapter(data)
	require.Len(t, files, 1)
	rendered := renderGeneratedFile(t, files[0])

	require.Contains(t, rendered, "ires, err := mcpC.ToolsCall()(ctx,")
	require.Contains(t, rendered, "r := ires.(*mcpCalc.ToolsCallResult)")
	require.NotContains(t, rendered, ".Recv(ctx)")
	require.NotContains(t, rendered, "ToolsCallClientStream")
}
