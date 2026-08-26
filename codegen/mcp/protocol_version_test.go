// Package codegen verifies that generated MCP clients and servers use the
// protocol version written by the service design.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/testutil"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

func TestInitializePayloadAcceptsClientProtocolVersion(t *testing.T) {
	svc, _ := testService("calc")
	mcp := &mcpexpr.MCPExpr{ProtocolVersion: "planned-version"}
	payload := newMCPExprBuilder(svc, mcp).buildInitializePayloadType()
	protocolVersion := expr.AsObject(payload.Type).Attribute("protocolVersion")

	require.Nil(t, protocolVersion.Validation)
	require.Contains(t, payload.Validation.Required, "protocolVersion")
}

func TestGenerateMCPTransport_RendersOneProtocolVersionContract(t *testing.T) {
	svc, _ := testService("calc", "add")
	mcp := &mcpexpr.MCPExpr{
		Name:            "calc",
		Version:         "1.0.0",
		ProtocolVersion: "2025-06-18",
	}
	data, err := newAdapterGenerator(
		svc,
		mcp,
	).buildAdapterData()
	require.NoError(t, err)
	data.MCPPackage = "mcpCalc"

	files := generateMCPTransport("example.com/calc/gen", svc, data)
	require.Len(t, files, 2)
	adapter := renderGeneratedFile(t, files[0])
	version := renderGeneratedFile(t, files[1])

	testutil.AssertGo(t, "testdata/golden/protocol_version/protocol_version.go.golden", version)
	require.Contains(t, version, `const DefaultProtocolVersion = "2025-06-18"`)
	require.NotContains(t, adapter, "if p.ProtocolVersion != DefaultProtocolVersion {")
	require.Contains(t, adapter, "ProtocolVersion: DefaultProtocolVersion,")
	require.NotContains(t, adapter, "ProtocolVersionOverride")
	require.NotContains(t, adapter, "mcpProtocolVersion")
}

func TestGenerateMCPClientSessionUsesNegotiatedHTTPContract(t *testing.T) {
	data := &AdapterData{
		mcpPathName: "mcp_calc",
		ClientSession: &ClientSessionData{
			MCPPackage:                "mcpcalc",
			JSONRPCPackage:            "jsonrpc",
			InitializedPayloadType:    "InitializedPayload",
			InitializedRequestBuilder: "BuildNotificationsInitializedRequest",
			InitializedRequestEncoder: "EncodeNotificationsInitializedRequest",
			HasTools:                  true,
			imports: []*codegen.ImportSpec{
				{Path: "context"},
				{Path: "errors"},
				{Path: "fmt"},
				{Path: "io"},
				{Path: "net/http"},
				{Path: "goa.design/goa-ai/runtime/mcp", Name: "mcpruntime"},
				{Path: "goa.design/goa/v3/jsonrpc"},
				{Path: "generated.local/gen/mcp_calc", Name: "mcpcalc"},
			},
		},
	}

	rendered := renderGeneratedFile(t, clientSessionFile(data))
	testutil.AssertGo(t, "testdata/golden/client_session/session.go.golden", rendered)
	require.Contains(t, rendered, "Capabilities: &mcpcalc.ClientCapabilities{}")
	require.Contains(t, rendered, "mcpruntime.NewHTTPSession(client.Doer, mcpcalc.DefaultProtocolVersion)")
	require.Contains(t, rendered, "if result.Capabilities.Tools == nil {")
	require.Contains(t, rendered, "if resp.StatusCode != http.StatusAccepted {")
}
