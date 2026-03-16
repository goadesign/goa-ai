package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	assistant "example.com/assistant/gen/assistant"
	mcpAssistantadapter "example.com/assistant/gen/mcp_assistant/adapter/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	"goa.design/goa/v3/jsonrpc"
)

func TestMCPClientAdapterMultiContentConcatenatesToolContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/rpc", r.URL.Path)

		var req jsonrpc.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "tools/call", req.Method)

		response := map[string]any{
			"jsonrpc": "2.0",
			"result": map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": "{\"result\":\"hello ",
					},
					{
						"type": "text",
						"text": "world!\"}",
					},
				},
			},
		}
		data, err := json.Marshal(response)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "text/event-stream")
		_, err = fmt.Fprintf(w, "event: response\n")
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "data: %s\n\n", data)
		require.NoError(t, err)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)

	endpoints := mcpAssistantadapter.NewEndpoints(
		u.Scheme,
		u.Host,
		server.Client(),
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)

	out, err := endpoints.MultiContent(context.Background(), &assistant.MultiContentPayload{Count: 2})
	require.NoError(t, err)

	result, ok := out.(*assistant.MultiContentResult)
	require.True(t, ok)
	require.NotNil(t, result.Result)
	assert.Equal(t, "hello world!", *result.Result)
}

func TestMCPClientAdapterUsesLaterTextContentWhenFirstItemIsNonText(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/rpc", r.URL.Path)

		var req jsonrpc.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "tools/call", req.Method)

		response := map[string]any{
			"jsonrpc": "2.0",
			"result": map[string]any{
				"content": []map[string]any{
					{
						"type":     "image",
						"data":     "ZmFrZS1pbWFnZQ==",
						"mimeType": "image/png",
					},
					{
						"type": "text",
						"text": "{\"result\":\"hello world from later text\"}",
					},
				},
			},
		}
		data, err := json.Marshal(response)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "text/event-stream")
		_, err = fmt.Fprintf(w, "event: response\n")
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "data: %s\n\n", data)
		require.NoError(t, err)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)

	endpoints := mcpAssistantadapter.NewEndpoints(
		u.Scheme,
		u.Host,
		server.Client(),
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		false,
	)

	out, err := endpoints.MultiContent(context.Background(), &assistant.MultiContentPayload{Count: 2})
	require.NoError(t, err)

	result, ok := out.(*assistant.MultiContentResult)
	require.True(t, ok)
	require.NotNil(t, result.Result)
	assert.Equal(t, "hello world from later text", *result.Result)
}
