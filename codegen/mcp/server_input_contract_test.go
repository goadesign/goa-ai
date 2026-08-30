// Package codegen verifies that generated MCP server adapters accept only the
// inputs declared by the original Goa methods.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
)

const (
	testCodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	testCodecPackage    = "mcpcodec"
)

func TestGenerateMCPTransportUsesOneExactURIForEachResource(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "read_document")
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", MimeType: "application/json", Method: methods["read_document"]},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
	).buildAdapterData()
	require.NoError(t, err)
	data.CodecImportPath = testCodecImportPath
	data.CodecPackage = testCodecPackage
	data.NeedsServerCodec = true
	data.Resources[0].ServiceMethodName = "ReadDocument"
	data.Resources[0].Codec = &MethodCodecData{
		ResultEncode: "EncodeReadDocumentResult",
	}

	files := generateMCPTransport("example.com/assistant/gen", svc, data)
	require.NotEmpty(t, files)
	rendered := renderGeneratedFile(t, files[0])

	require.Contains(t, rendered, `switch p.URI`)
	require.Contains(t, rendered, `case "doc://list":`)
	require.Contains(t, rendered, `result, err := a.service.ReadDocument(ctx)`)
	require.NotContains(t, rendered, "ParseQuery")
	require.NotContains(t, rendered, "PayloadTransport")
}

func TestGenerateMCPTransportRejectsInputForMethodsWithoutPayloads(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "run", "read_status")
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "run", Method: methods["run"]},
		},
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "status", URI: "status://current", MimeType: "application/json", Method: methods["read_status"]},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
	).buildAdapterData()
	require.NoError(t, err)
	data.CodecImportPath = testCodecImportPath
	data.CodecPackage = testCodecPackage
	data.NeedsServerCodec = true
	data.Tools[0].Codec = &MethodCodecData{ResultEncode: "EncodeRunResult"}
	data.Resources[0].Codec = &MethodCodecData{ResultEncode: "EncodeReadStatusResult"}

	files := generateMCPTransport("example.com/assistant/gen", svc, data)
	require.NotEmpty(t, files)
	rendered := renderGeneratedFile(t, files[0])

	require.Contains(t, rendered, "if len(arguments) == 0 {")
	require.Contains(t, rendered, "if fields == nil || len(fields) > 0 {")
	require.Contains(t, rendered, `if err := validateNoArguments(p.Arguments); err != nil {`)
	require.Contains(t, rendered, `return nil, goa.PermanentError("invalid_params", "invalid arguments for tool %s: %s", p.Name, err.Error())`)
	require.NotContains(t, rendered, `toolCallError("invalid arguments`)
	require.Contains(t, rendered, `switch p.URI`)
	require.NotContains(t, rendered, "ParseQuery")
}
