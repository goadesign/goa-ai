package httpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/a2a"
)

// TestSendTaskSuccess verifies that SendTask issues a JSON-RPC request with the
// expected method and parameters and returns the raw result payload unchanged.
func TestSendTaskSuccess(t *testing.T) {
	t.Helper()

	var captured rpcRequest

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", http.StatusBadRequest)
			return
		}

		defer func() { _ = r.Body.Close() }()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := rpcResponse{
			JSONRPC: "2.0",
			Result:  json.RawMessage(`{"ok":true}`),
			ID:      captured.ID,
		}
		if err := json.NewEncoder(w).Encode(&resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := New(server.URL)
	require.NoError(t, err)

	payload := json.RawMessage(`{"msg":"hello"}`)
	resp, err := client.SendTask(context.Background(), a2a.SendTaskRequest{
		Suite:   "svc.agent.tools",
		Skill:   "tools.echo",
		Payload: payload,
	})
	require.NoError(t, err)

	require.Equal(t, "svc.agent.tools", captured.Params.(map[string]any)["suite"])
	require.Equal(t, "tools.echo", captured.Params.(map[string]any)["skill"])

	out, ok := captured.Params.(map[string]any)["payload"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "hello", out["msg"])

	require.JSONEq(t, `{"ok":true}`, string(resp.Result))
}

// TestSendTaskJSONRPCErrorMapping verifies that JSON-RPC errors are converted
// into the public a2a.Error type with matching code and message.
func TestSendTaskJSONRPCErrorMapping(t *testing.T) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()

		resp := rpcResponse{
			JSONRPC: "2.0",
			Error: &rpcError{
				Code:    a2a.JSONRPCInvalidParams,
				Message: "invalid params",
			},
			ID: 1,
		}
		if err := json.NewEncoder(w).Encode(&resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := New(server.URL)
	require.NoError(t, err)

	_, err = client.SendTask(context.Background(), a2a.SendTaskRequest{
		Suite:   "svc.agent.tools",
		Skill:   "tools.echo",
		Payload: json.RawMessage(`{"msg":"bad"}`),
	})
	require.Error(t, err)

	var a2aErr *a2a.Error
	require.ErrorAs(t, err, &a2aErr)
	require.Equal(t, a2a.JSONRPCInvalidParams, a2aErr.Code)
	require.Equal(t, "invalid params", a2aErr.Message)
}

// TestWithHeaderAndBearerToken verifies that auth-related options attach headers.
func TestWithHeaderAndBearerToken(t *testing.T) {
	t.Helper()

	var authHeader string
	var apiKey string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		apiKey = r.Header.Get("X-API-Key")

		resp := rpcResponse{
			JSONRPC: "2.0",
			Result:  json.RawMessage(`{}`),
			ID:      1,
		}
		if err := json.NewEncoder(w).Encode(&resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := New(server.URL,
		WithBearerToken("secret-token"),
		WithHeader("X-API-Key", "apikey"),
	)
	require.NoError(t, err)

	_, err = client.SendTask(context.Background(), a2a.SendTaskRequest{
		Suite:   "svc.agent.tools",
		Skill:   "tools.echo",
		Payload: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	require.Equal(t, "Bearer secret-token", authHeader)
	require.Equal(t, "apikey", apiKey)
}


