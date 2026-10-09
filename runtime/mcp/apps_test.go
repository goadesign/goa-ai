// These tests check external Apps declarations and prove that HTTP and stdio
// model callers reject app-only tools before the server can execute them.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolModelVisibility(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     string
		allowed bool
		invalid bool
	}{
		{name: "absent", allowed: true},
		{name: "extension", raw: `{"other/extension":{"value":1}}`, allowed: true},
		{name: "default", raw: `{"ui":{"resourceUri":"ui://records/panel"}}`, allowed: true},
		{name: "both", raw: `{"ui":{"visibility":["app","model"]}}`, allowed: true},
		{name: "model", raw: `{"ui":{"visibility":["model"]}}`, allowed: true},
		{name: "app", raw: `{"ui":{"visibility":["app"]}}`},
		{name: "null metadata", raw: `null`, invalid: true},
		{name: "null UI", raw: `{"ui":null}`, invalid: true},
		{name: "null URI", raw: `{"ui":{"resourceUri":null}}`, invalid: true},
		{name: "external URI", raw: `{"ui":{"resourceUri":"https://example.test/panel"}}`, invalid: true},
		{name: "opaque URI", raw: `{"ui":{"resourceUri":"ui:panel"}}`, invalid: true},
		{name: "empty URI", raw: `{"ui":{"resourceUri":"ui://"}}`, invalid: true},
		{name: "null visibility", raw: `{"ui":{"visibility":null}}`, invalid: true},
		{name: "empty visibility", raw: `{"ui":{"visibility":[]}}`, invalid: true},
		{name: "unknown caller", raw: `{"ui":{"visibility":["browser"]}}`, invalid: true},
		{name: "repeated caller", raw: `{"ui":{"visibility":["model","model"]}}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			allowed, err := toolModelVisibility(json.RawMessage(test.raw))
			assert.Equal(t, test.invalid, err != nil)
			assert.Equal(t, test.allowed, allowed)
		})
	}
}

func TestHTTPCallerChecksAppsVisibilityBeforeExecution(t *testing.T) {
	for _, test := range []struct {
		name, meta string
		rejected   bool
	}{
		{name: "ordinary"},
		{name: "model", meta: `{"ui":{"visibility":["model"]}}`},
		{name: "both", meta: `{"ui":{"visibility":["model","app"]}}`},
		{name: "app", meta: `{"ui":{"visibility":["app"]}}`, rejected: true},
		{name: "invalid", meta: `{"ui":{"visibility":[]}}`, rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var request struct {
					ID     json.RawMessage
					Method string
				}
				if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&request)) {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var result any
				switch request.Method {
				case "tools/list":
					tool := map[string]any{"name": "read", "inputSchema": json.RawMessage(`{"type":"object"}`)}
					if test.meta != "" {
						tool["_meta"] = json.RawMessage(test.meta)
					}
					result = map[string]any{"resultType": "complete", "ttlMs": 0, "cacheScope": "private", "tools": []any{tool}}
				case "tools/call":
					calls++
					result = map[string]any{"resultType": "complete", "content": []any{}, "structuredContent": "read"}
				default:
					t.Errorf("unexpected method %q", request.Method)
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}))
			}))
			t.Cleanup(server.Close)
			caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "apps-test", Version: "1"}})
			require.NoError(t, err)
			_, err = caller.CallTool(t.Context(), CallRequest{Tool: "read", Payload: json.RawMessage(`{}`)})
			assert.Equal(t, test.rejected, err != nil)
			if test.rejected {
				assert.Zero(t, calls)
			} else {
				assert.Equal(t, 1, calls)
			}
		})
	}
}

func TestStdioCallerChecksAppsVisibilityAndSchemas(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{Command: os.Args[0], Args: []string{"-test.run=^TestStdioProtocolPeer$"}, Env: []string{"GOA_AI_MCP_PROTOCOL_PEER=1"}, ClientInfo: ClientInfo{Name: "apps-test", Version: "1"}, InputSupport: InputSupport{Form: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})
	_, err = caller.CallTool(t.Context(), CallRequest{Tool: "app-only", Payload: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "app-only")
	_, err = caller.CallTool(t.Context(), CallRequest{Tool: "first", Payload: json.RawMessage(`{"extra":true}`)})
	require.ErrorContains(t, err, "arguments")
	response, err := caller.CallTool(t.Context(), CallRequest{Tool: "first", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"first"`, string(response.StructuredContent))
}
