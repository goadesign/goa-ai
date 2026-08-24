// Package codegen verifies that generated MCP server adapters accept only the query
// fields and arguments declared by the original Goa methods.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	gcodegen "goa.design/goa/v3/codegen"
)

const (
	testCodecImportPath = "example.com/assistant/gen/mcp_assistant/internal/codec"
	testCodecPackage    = "mcpcodec"
)

func TestGenerateMCPTransportParsesResourceQueryFieldsByDeclaredType(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "read_document")
	methods["read_document"].Payload = testResourceQueryPayload()
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "documents", URI: "doc://list", Method: methods["read_document"]},
		},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
		nil,
		newMCPExprBuilder(svc, mcp, nil).BuildServiceMapping(),
	).buildAdapterData()
	require.NoError(t, err)
	data.CodecImportPath = testCodecImportPath
	data.CodecPackage = testCodecPackage
	data.NeedsServerCodec = true
	data.Resources[0].Codec = &MethodCodecData{
		PayloadNew:       "NewReadDocumentPayload",
		PayloadTransport: "mcpcodec.ReadDocumentPayloadTransport",
		ResultEncode:     "EncodeReadDocumentResult",
	}
	setTestResourceTransportFields(data.Resources[0].QueryFields)

	files := generateMCPTransport("example.com/assistant/gen", svc, data)
	require.NotEmpty(t, files)
	rendered := renderGeneratedFile(t, files[0])

	require.NotContains(t, rendered, "CoerceQuery")
	require.NotContains(t, rendered, "parseQueryParamsToJSON")
	require.NotContains(t, rendered, "Field0")
	require.NotContains(t, rendered, "json.Marshal(&arguments)")
	require.NotContains(t, rendered, "encode resource query")
	require.NotContains(t, rendered, "func validateNoArguments(")
	require.Contains(t, rendered, "query, err := url.ParseQuery(resourceURI.RawQuery)")
	require.Contains(t, rendered, "transport := new(mcpcodec.ReadDocumentPayloadTransport)")
	require.Contains(t, rendered, "switch name {")
	require.Contains(t, rendered, `case "cursor":`)
	require.Contains(t, rendered, `transport.Cursor = &converted`)
	require.Contains(t, rendered, `case "tags":`)
	require.Contains(t, rendered, `parsed := make([]*mcpcodec.TagAliasTransport, len(values))`)
	require.Contains(t, rendered, `transport.Tags = parsed`)
	require.Contains(t, rendered, `case "enabled":`)
	require.Contains(t, rendered, `value, err := strconv.ParseBool(values[0])`)
	require.Contains(t, rendered, `case "offset":`)
	require.Contains(t, rendered, `value, err := strconv.ParseInt(values[0], 10, strconv.IntSize)`)
	require.Contains(t, rendered, `case "limit":`)
	require.Contains(t, rendered, `value, err := strconv.ParseUint(values[0], 10, strconv.IntSize)`)
	require.Contains(t, rendered, `case "ratio":`)
	require.Contains(t, rendered, `value, err := strconv.ParseFloat(values[0], 64)`)
	require.Contains(t, rendered, `payload, err := mcpcodec.NewReadDocumentPayload(transport)`)
	require.Contains(t, rendered, `return nil, goa.PermanentError("invalid_params", "unknown query parameter %q", name)`)
}

// setTestResourceTransportFields supplies the private codec names that the
// full generator normally links after Goa has chosen every package name.
func setTestResourceTransportFields(fields []*ResourceQueryField) {
	for _, field := range fields {
		field.TransportSelector = gcodegen.Goify(field.QueryKey, true)
		field.TransportType = "*" + field.ValueType
		field.TransportValueType = field.ValueType
		field.TransportPointer = true
		if field.Repeated {
			field.TransportType = "[]*mcpcodec.TagAliasTransport"
			field.TransportElementType = "mcpcodec.TagAliasTransport"
			field.TransportElementPointer = true
		}
	}
}

func TestGenerateMCPTransportRejectsInputForMethodsWithoutPayloads(t *testing.T) {
	restore := resetMCPCodegenState(t)
	defer restore()

	svc, methods := testService("assistant", "run", "read_status", "write_prompt")
	mcp := &mcpexpr.MCPExpr{
		Name:    "assistant-mcp",
		Version: "1.0.0",
		Tools: []*mcpexpr.ToolExpr{
			{Name: "run", Method: methods["run"]},
		},
		Resources: []*mcpexpr.ResourceExpr{
			{Name: "status", URI: "status://current", Method: methods["read_status"]},
		},
	}
	prompts := []*mcpexpr.DynamicPromptExpr{
		{Name: "write_prompt", Method: methods["write_prompt"]},
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
		prompts,
		newMCPExprBuilder(svc, mcp, prompts).BuildServiceMapping(),
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
	require.Contains(t, rendered, `return nil, goa.PermanentError("invalid_params", "tool %q does not accept arguments: %s", p.Name, err)`)
	require.Contains(t, rendered, `return nil, goa.PermanentError("invalid_params", "prompt %q does not accept arguments: %s", p.Name, err)`)
	require.Contains(t, rendered, "if resourceURI.ForceQuery || len(query) > 0 {")
	require.Contains(t, rendered, `return nil, goa.PermanentError("invalid_params", "resource %q does not accept query parameters", baseURI)`)
}
