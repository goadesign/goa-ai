// These tests check the older HTTP envelope before generated endpoints run.
// Negotiation accepts a different requested version, while later requests must
// name the implemented revision and cannot carry modern execution controls.
package mcp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyHTTPRequestBoundary(t *testing.T) {
	handshake := `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"peer","version":"1"}}`
	for _, test := range []struct {
		name, method, params, version string
		code                          int
	}{
		{name: "initialize without header", method: "initialize", params: handshake},
		{name: "initialize unknown version", method: "initialize", params: strings.Replace(handshake, "2025-11-25", "2099-01-01", 1)},
		{name: "initialize null capabilities", method: "initialize", params: strings.Replace(handshake, `"capabilities":{}`, `"capabilities":null`, 1), code: JSONRPCInvalidParams},
		{name: "initialize missing client", method: "initialize", params: `{"protocolVersion":"2025-11-25","capabilities":{}}`, code: JSONRPCInvalidParams},
		{name: "list without params", method: "tools/list", version: LegacyProtocolVersion},
		{name: "ping", method: "ping", version: LegacyProtocolVersion},
		{name: "missing version", method: "tools/list", code: JSONRPCInvalidRequest},
		{name: "wrong version", method: "tools/list", version: ProtocolVersion, code: JSONRPCInvalidRequest},
		{name: "null params", method: "tools/list", params: `null`, version: LegacyProtocolVersion, code: JSONRPCInvalidRequest},
		{name: "tool arguments", method: "tools/call", params: `{"name":"read","arguments":{"name":"record"}}`, version: LegacyProtocolVersion},
		{name: "task creation", method: "tools/call", params: `{"name":"read","arguments":{},"task":{}}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "input round", method: "tools/call", params: `{"name":"read","requestState":"state"}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "input response", method: "tools/call", params: `{"name":"read","inputResponses":{}}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "mixed version metadata", method: "tools/list", params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "mixed capability metadata", method: "tools/list", params: `{"_meta":{"io.modelcontextprotocol/clientCapabilities":{}}}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "invalid progress", method: "tools/list", params: `{"_meta":{"progressToken":null}}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "null prompt argument", method: "prompts/get", params: `{"name":"welcome","arguments":{"label":null}}`, version: LegacyProtocolVersion, code: JSONRPCInvalidParams},
		{name: "subscription", method: "subscriptions/listen", params: `{}`, version: LegacyProtocolVersion, code: JSONRPCMethodNotFound},
		{name: "task lookup", method: "tasks/get", params: `{}`, version: LegacyProtocolVersion, code: JSONRPCMethodNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":9007199254740993,"method":"` + test.method + `"`
			if test.params != "" {
				body += `,"params":` + test.params
			}
			body += `}`
			request := httptest.NewRequestWithContext(t.Context(), "POST", "https://resource.example/mcp", strings.NewReader(body))
			if test.version != "" {
				request.Header.Set("MCP-Protocol-Version", test.version)
			}
			decoded, failure := DecodeLegacyHTTPRequest(request, []byte(body))
			if test.code != 0 {
				require.NotNil(t, failure)
				assert.Equal(t, test.code, failure.Code)
				assert.Nil(t, decoded)
				return
			}
			require.Nil(t, failure)
			assert.Equal(t, json.Number("9007199254740993"), decoded.ID)
			var params map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(decoded.Params, &params))
			var meta map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(params["_meta"], &meta))
			assert.JSONEq(t, `"2025-11-25"`, string(meta[protocolVersionKey]))
			assert.JSONEq(t, `{}`, string(meta[clientCapabilitiesKey]))
			if test.method == "initialize" {
				assert.Len(t, params, 1)
			}
			if test.name == "tool arguments" {
				assert.JSONEq(t, `{"name":"record"}`, string(params["arguments"]))
			}
		})
	}
}

func TestLegacyHTTPRequestSelection(t *testing.T) {
	for _, test := range []struct {
		name, body, version string
		legacy              bool
	}{
		{"handshake", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "", true},
		{"older header", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, LegacyProtocolVersion, true},
		{"modern header", `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`, ProtocolVersion, false},
		{"modern initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, ProtocolVersion, false},
		{"unsupported modern initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01"}}}`, "2099-01-01", false},
		{"older header with modern metadata", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, LegacyProtocolVersion, true},
		{"malformed body", `{`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), "POST", "https://resource.example/mcp", strings.NewReader(test.body))
			request.Header.Set("MCP-Protocol-Version", test.version)
			assert.Equal(t, test.legacy, IsLegacyHTTPRequest(request, []byte(test.body)))
		})
	}
}

func TestLegacyHTTPRequestIDsAndNotifications(t *testing.T) {
	for _, id := range []string{`0`, `9007199254740993`, `"call"`, `""`} {
		body := `{"jsonrpc":"2.0","id":` + id + `,"method":"ping"}`
		request := httptest.NewRequestWithContext(t.Context(), "POST", "https://resource.example/mcp", strings.NewReader(body))
		request.Header.Set("MCP-Protocol-Version", LegacyProtocolVersion)
		decoded, failure := DecodeLegacyHTTPRequest(request, []byte(body))
		require.Nil(t, failure)
		encoded, err := json.Marshal(decoded.ID)
		require.NoError(t, err)
		assert.Equal(t, id, string(encoded))
	}
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	request := httptest.NewRequestWithContext(t.Context(), "POST", "https://resource.example/mcp", strings.NewReader(body))
	request.Header.Set("MCP-Protocol-Version", LegacyProtocolVersion)
	decoded, failure := DecodeLegacyHTTPRequest(request, []byte(body))
	require.Nil(t, failure)
	assert.False(t, decoded.HasID)
	request.Header.Add("MCP-Protocol-Version", LegacyProtocolVersion)
	_, failure = DecodeLegacyHTTPRequest(request, []byte(body))
	require.NotNil(t, failure)
	assert.Equal(t, JSONRPCInvalidRequest, failure.Code)
}
