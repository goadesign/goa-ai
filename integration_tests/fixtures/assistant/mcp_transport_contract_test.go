// This file verifies that the generated JSON-RPC transport preserves MCP prompt arguments.
package assistantapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	genmcpassistantsrv "example.com/assistant/gen/jsonrpc/mcp_assistant/server"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	genjsonrpc "goa.design/goa/v3/jsonrpc"
)

// TestPromptsGetTransportPreservesArguments verifies that the adapter receives
// the exact string arguments sent by an MCP client.
func TestPromptsGetTransportPreservesArguments(t *testing.T) {
	request := httptest.NewRequest("POST", "/rpc", nil)
	raw := &genjsonrpc.RawRequest{
		Params: json.RawMessage(`{"name":"code_review","arguments":{"code":"x"}}`),
	}

	payload, err := genmcpassistantsrv.DecodePromptsGetRequest(nil, goahttp.RequestDecoder)(request, raw)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"code": "x"}, payload.Arguments)
}
