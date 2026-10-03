// These tests use an independent HTTP peer to check catalog pagination,
// schema validation, and exact request headers without an initialization call.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/mcpprotocol"
)

func TestHTTPCallerValidatesPaginatedCatalogAndRecursiveResults(t *testing.T) {
	inputSchema := json.RawMessage(`{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"},"tree":{"$ref":"#/$defs/node"}},"required":["region","tree"],"additionalProperties":false,"$defs":{"node":{"type":"object","properties":{"value":{"type":"integer"},"next":{"$ref":"#/$defs/node"}},"required":["value"],"additionalProperties":false}}}`)
	outputSchema := json.RawMessage(`{"oneOf":[{"type":"null"},{"type":"array","items":{"type":"integer"}}]}`)
	bindings, err := mcpprotocol.CompileHeaderBindings(inputSchema)
	require.NoError(t, err)
	var methods []string
	result := json.RawMessage(`null`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var message struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&message)) {
			return
		}
		methods = append(methods, message.Method)
		assert.Equal(t, ProtocolVersion, req.Header.Get("MCP-Protocol-Version"))
		assert.Equal(t, message.Method, req.Header.Get("MCP-Method"))
		assert.NotEmpty(t, message.Params["_meta"])
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch message.Method {
		case "tools/list":
			if _, paginated := message.Params["cursor"]; !paginated {
				response = map[string]any{"resultType": "complete", "ttlMs": 0.5, "cacheScope": "private", "nextCursor": "page-2", "tools": []any{
					map[string]any{"name": "broken", "inputSchema": json.RawMessage(`{"type":"object","properties":{"array":{"type":"array","items":{"type":"string","x-mcp-header":"Bad"}}}}`)},
				}}
			} else {
				assert.JSONEq(t, `"page-2"`, string(message.Params["cursor"]))
				response = map[string]any{"resultType": "complete", "ttlMs": 0, "cacheScope": "private", "tools": []any{
					map[string]any{"name": " café ", "inputSchema": inputSchema, "outputSchema": outputSchema},
				}}
			}
		case "tools/call":
			values, err := mcpprotocol.ParameterValues(message.Params["arguments"], bindings)
			if !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, values["Mcp-Param-Region"], req.Header.Get("Mcp-Param-Region"))
			assert.Equal(t, mcpprotocol.EncodeHeaderValue(" café "), req.Header.Get("Mcp-Name"))
			response = map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": result}
		default:
			t.Errorf("unexpected method %q", message.Method)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": response}))
	}))
	t.Cleanup(server.Close)
	caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}})
	require.NoError(t, err)
	assert.Empty(t, methods)
	request := CallRequest{Tool: " café ", Payload: json.RawMessage(`{"region":" café ","tree":{"value":1,"next":{"value":2}}}`)}
	response, err := caller.CallTool(context.Background(), request)
	require.NoError(t, err)
	assert.JSONEq(t, `null`, string(response.StructuredContent))
	assert.Equal(t, []string{"tools/list", "tools/list", "tools/call"}, methods)
	methods = nil
	request.Payload = json.RawMessage(`{"region":" café ","tree":{"value":1,"next":{"value":"wrong"}}}`)
	_, err = caller.CallTool(context.Background(), request)
	require.ErrorContains(t, err, "arguments")
	assert.Equal(t, []string{"tools/list", "tools/list"}, methods)
	methods = nil
	request.Payload = json.RawMessage(`{"region":" café ","tree":{"value":1}}`)
	result = json.RawMessage(`{"wrong":"shape"}`)
	_, err = caller.CallTool(context.Background(), request)
	require.Error(t, err)
	var malformed *MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, []string{"tools/list", "tools/list", "tools/call"}, methods)
}

func TestHeaderAnnotationsIgnoreExampleValues(t *testing.T) {
	bindings, err := mcpprotocol.CompileHeaderBindings(json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","x-mcp-header":"Value"}},"examples":[{"x-mcp-header":"This is user data"}],"default":{"x-mcp-header":"Also data"}}`))
	require.NoError(t, err)
	assert.Equal(t, []HeaderBinding{{Name: "Value", Path: []string{"value"}, Type: "string"}}, bindings)
	for _, contract := range []string{
		`{"type":"object","$defs":{"hidden":{"type":"string","x-mcp-header":"Hidden"}}}`,
		`{"type":"object","properties":{"notObject":{"type":"array","properties":{"hidden":{"type":"string","x-mcp-header":"Hidden"}}}}}`,
		`{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"Name"},"b":{"type":"string","x-mcp-header":"name"}}}`,
		`{"type":"string"}`,
	} {
		_, err := mcpprotocol.CompileHeaderBindings(json.RawMessage(contract))
		assert.Error(t, err, contract)
	}
}
