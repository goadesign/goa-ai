// This file checks initialization, connection headers, notification responses,
// and JSON or server-sent event results used by the HTTP caller.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPCallerCompletesInitializationBeforeReturning(t *testing.T) {
	t.Parallel()

	var requests []map[string]json.RawMessage
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			var request map[string]json.RawMessage
			if err := json.Unmarshal(body, &request); err != nil {
				return nil, err
			}
			requests = append(requests, request)
			var method string
			if err := json.Unmarshal(request["method"], &method); err != nil {
				return nil, err
			}
			switch method {
			case rpcMethodInitialize:
				var id uint64
				if err := json.Unmarshal(request["id"], &id); err != nil {
					return nil, err
				}
				response := rpcResponse{
					JSONRPC: "2.0",
					ID:      id,
					Result:  json.RawMessage(validInitializeResultJSON),
				}
				data, err := json.Marshal(response)
				if err != nil {
					return nil, err
				}
				return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
			case rpcMethodInitialized:
				return httpResponse(http.StatusAccepted, nil, nil), nil
			default:
				return httpResponse(http.StatusBadRequest, nil, nil), nil
			}
		}),
	}

	_, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})

	require.NoError(t, err)
	require.Len(t, requests, 2)
	require.JSONEq(t, `"initialize"`, string(requests[0]["method"]))
	require.Contains(t, requests[0], "id")
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(requests[0]["params"], &params))
	require.JSONEq(t, `{}`, string(params["capabilities"]))
	require.JSONEq(t, `"notifications/initialized"`, string(requests[1]["method"]))
	require.NotContains(t, requests[1], "id")
}

func TestHTTPCallerDefaultProtocolVersion(t *testing.T) {
	t.Parallel()

	var protocolVersion string
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, err
			}
			if req.Method == rpcMethodInitialized {
				return httpResponse(http.StatusAccepted, nil, nil), nil
			}
			params := req.Params.(map[string]any)
			protocolVersion = params["protocolVersion"].(string)
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(validInitializeResultJSON)}
			data, err := json.Marshal(resp)
			if err != nil {
				return nil, err
			}
			return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
		}),
	}

	_, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})
	require.NoError(t, err)
	require.Equal(t, "2025-06-18", protocolVersion)
}

func TestHTTPCallerRequiresClientInfoBeforeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clientInfo ClientInfo
		wantError  string
	}{
		{name: "missing name", clientInfo: ClientInfo{Version: "2.4.1"}, wantError: "mcp: client name is required"},
		{name: "missing version", clientInfo: ClientInfo{Name: "operations-agent"}, wantError: "mcp: client version is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &http.Client{
				Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("HTTP request made with incomplete client identity")
					return nil, nil
				}),
			}
			_, err := NewHTTPCaller(context.Background(), HTTPOptions{
				Endpoint:   "http://mcp.test/rpc",
				Client:     client,
				ClientInfo: test.clientInfo,
			})
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestHTTPCallerSendsClientInfo(t *testing.T) {
	t.Parallel()

	var gotName, gotVersion string
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, err
			}
			if req.Method == rpcMethodInitialized {
				return httpResponse(http.StatusAccepted, nil, nil), nil
			}
			params := req.Params.(map[string]any)
			clientInfo := params["clientInfo"].(map[string]any)
			gotName = clientInfo["name"].(string)
			gotVersion = clientInfo["version"].(string)
			resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(validInitializeResultJSON)}
			data, err := json.Marshal(resp)
			if err != nil {
				return nil, err
			}
			return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
		}),
	}

	_, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})
	require.NoError(t, err)
	require.Equal(t, "operations-agent", gotName)
	require.Equal(t, "2.4.1", gotVersion)
}

func TestHTTPCallerRequiresAbsoluteHTTPEndpointBeforeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		endpoint  string
		wantError string
	}{
		{name: "empty", wantError: "mcp: HTTP endpoint is required"},
		{name: "relative", endpoint: "/mcp", wantError: `mcp: invalid HTTP endpoint "/mcp"`},
		{name: "scheme relative", endpoint: "//mcp.example/rpc", wantError: `mcp: invalid HTTP endpoint "//mcp.example/rpc"`},
		{name: "FTP", endpoint: "ftp://mcp.example/rpc", wantError: `mcp: invalid HTTP endpoint "ftp://mcp.example/rpc"`},
		{name: "other scheme", endpoint: "file:///tmp/mcp", wantError: `mcp: invalid HTTP endpoint "file:///tmp/mcp"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &http.Client{
				Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("HTTP request made with an invalid endpoint")
					return nil, nil
				}),
			}
			_, err := NewHTTPCaller(context.Background(), HTTPOptions{
				Endpoint:   test.endpoint,
				Client:     client,
				ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
			})
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestHTTPCallerAcceptsAbsoluteHTTPEndpoint(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{"http://mcp.example/rpc", "https://mcp.example/rpc"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()

			client := &http.Client{
				Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					require.Equal(t, endpoint, r.URL.String())
					var req rpcRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
					if req.Method == rpcMethodInitialized {
						return httpResponse(http.StatusAccepted, nil, nil), nil
					}
					resp := rpcResponse{JSONRPC: "2.0", ID: 1, Result: json.RawMessage(validInitializeResultJSON)}
					data, err := json.Marshal(resp)
					if err != nil {
						return nil, err
					}
					return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
				}),
			}
			_, err := NewHTTPCaller(context.Background(), HTTPOptions{
				Endpoint:   endpoint,
				Client:     client,
				ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
			})
			require.NoError(t, err)
		})
	}
}

func TestHTTPCallerRejectsIncompleteInitializeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    string
		wantError string
	}{
		{name: "missing protocol version", result: `{"serverInfo":{"name":"server","version":"1.0.0"},"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response protocolVersion is required"},
		{name: "missing server info", result: `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo is required"},
		{name: "null server info", result: `{"protocolVersion":"2025-06-18","serverInfo":null,"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo is required"},
		{name: "missing server name", result: `{"protocolVersion":"2025-06-18","serverInfo":{"version":"1.0.0"},"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo.name is required"},
		{name: "missing server version", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server"},"capabilities":{"tools":{}}}`, wantError: "mcp: initialize response serverInfo.version is required"},
		{name: "missing capabilities", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"}}`, wantError: "mcp: initialize response capabilities are required"},
		{name: "null capabilities", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"},"capabilities":null}`, wantError: "mcp: initialize response capabilities are required"},
		{name: "missing tools capability", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"},"capabilities":{}}`, wantError: "mcp: initialize response tools capability is required"},
		{name: "null tools capability", result: `{"protocolVersion":"2025-06-18","serverInfo":{"name":"server","version":"1.0.0"},"capabilities":{"tools":null}}`, wantError: "mcp: initialize response tools capability is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requests := 0
			client := &http.Client{
				Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					requests++
					var req rpcRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						return nil, err
					}
					response := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(test.result)}
					data, err := json.Marshal(response)
					if err != nil {
						return nil, err
					}
					return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
				}),
			}
			_, err := NewHTTPCaller(context.Background(), HTTPOptions{
				Endpoint:   "http://mcp.test/rpc",
				Client:     client,
				ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
			})
			require.EqualError(t, err, "mcp initialize failed: "+test.wantError)
			require.Equal(t, 1, requests)
		})
	}
}

func TestHTTPCallerSendsStreamableHTTPHeadersAfterInitialization(t *testing.T) {
	t.Parallel()

	const sessionID = "mcp-session-42"
	var requests []*http.Request
	client := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			requests = append(requests, r.Clone(r.Context()))
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, err
			}
			switch req.Method {
			case rpcMethodInitialize:
				response := rpcResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(validInitializeResultJSON),
				}
				data, err := json.Marshal(response)
				if err != nil {
					return nil, err
				}
				return httpResponse(http.StatusOK, http.Header{
					"Content-Type":   []string{"application/json"},
					"Mcp-Session-Id": []string{sessionID},
				}, data), nil
			case rpcMethodInitialized:
				return httpResponse(http.StatusAccepted, nil, nil), nil
			case rpcMethodToolsCall:
				response := rpcResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
				}
				data, err := json.Marshal(response)
				if err != nil {
					return nil, err
				}
				return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
			default:
				return httpResponse(http.StatusBadRequest, nil, nil), nil
			}
		}),
	}

	caller, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})
	require.NoError(t, err)
	_, err = caller.CallTool(context.Background(), CallRequest{Tool: "search", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Len(t, requests, 3)
	for _, request := range requests {
		require.Equal(t, "application/json, text/event-stream", request.Header.Get("Accept"))
	}
	require.Empty(t, requests[0].Header.Get("MCP-Protocol-Version"))
	require.Empty(t, requests[0].Header.Get("Mcp-Session-Id"))
	for _, request := range requests[1:] {
		require.Equal(t, "2025-06-18", request.Header.Get("MCP-Protocol-Version"))
		require.Equal(t, sessionID, request.Header.Get("Mcp-Session-Id"))
	}
}

func TestHTTPCallerRequiresEmptyAcceptedNotificationResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		body      []byte
		wantError string
	}{
		{name: "wrong status", status: http.StatusOK, wantError: "mcp rpc status 200"},
		{name: "response body", status: http.StatusAccepted, body: []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), wantError: "mcp: notification response must be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := notificationHTTPClient(func() *http.Response {
				return httpResponse(test.status, nil, test.body)
			})
			_, err := NewHTTPCaller(context.Background(), HTTPOptions{
				Endpoint:   "http://mcp.test/rpc",
				Client:     client,
				ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
			})
			require.EqualError(t, err, "mcp initialize failed: "+test.wantError)
		})
	}
}

func TestHTTPCallerDecodesMatchingEventStreamResponse(t *testing.T) {
	t.Parallel()

	client := initializedHTTPClient(func() *http.Response {
		body := []byte("event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":99,\"result\":{}}\n\n" +
			": keepalive\n\n" +
			"event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n" +
			"event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":\"server-ping-1\",\"method\":\"ping\",\"params\":{}}\n\n" +
			"event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"sampling/createMessage\",\"params\":{}}\n\n" +
			"event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"streamed\"}]}}\n\n")
		return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}, body)
	})
	caller, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})
	require.NoError(t, err)

	response, err := caller.CallTool(context.Background(), CallRequest{Tool: "search", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Equal(t, []string{"streamed"}, response.Content)
}

func TestHTTPCallerStartsNewSessionAfterSessionNotFound(t *testing.T) {
	t.Parallel()

	var methods []string
	initializations := 0
	toolCalls := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			message := decodeTestRPCMessage(t, req)
			methods = append(methods, message.Method)
			switch message.Method {
			case rpcMethodInitialize:
				initializations++
				require.Empty(t, req.Header.Get("MCP-Protocol-Version"))
				require.Empty(t, req.Header.Get("Mcp-Session-Id"))
				response := rpcReply{
					JSONRPC: "2.0",
					ID:      message.ID,
					Result:  json.RawMessage(validInitializeResultJSON),
				}
				body, err := json.Marshal(response)
				require.NoError(t, err)
				return httpResponse(http.StatusOK, http.Header{
					"Content-Type":   []string{"application/json"},
					"Mcp-Session-Id": []string{fmt.Sprintf("session-%d", initializations)},
				}, body), nil
			case rpcMethodInitialized:
				require.Equal(t, fmt.Sprintf("session-%d", initializations), req.Header.Get("Mcp-Session-Id"))
				return httpResponse(http.StatusAccepted, nil, nil), nil
			case rpcMethodToolsCall:
				toolCalls++
				require.Equal(t, "session-1", req.Header.Get("Mcp-Session-Id"))
				return httpResponse(http.StatusNotFound, nil, []byte("expired")), nil
			default:
				return httpResponse(http.StatusBadRequest, nil, nil), nil
			}
		}),
	}
	caller, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})
	require.NoError(t, err)

	_, err = caller.CallTool(context.Background(), CallRequest{Tool: "search", Payload: json.RawMessage(`{}`)})
	require.EqualError(t, err, "mcp rpc status 404")
	require.Equal(t, 2, initializations)
	require.Equal(t, 1, toolCalls)
	require.Equal(t, []string{
		rpcMethodInitialize,
		rpcMethodInitialized,
		rpcMethodToolsCall,
		rpcMethodInitialize,
		rpcMethodInitialized,
	}, methods)
}

func TestHTTPCallerStartsNewSessionWhenServerRequestReplyFindsExpiredSession(t *testing.T) {
	t.Parallel()

	initializations := 0
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			message := decodeTestRPCMessage(t, req)
			switch message.Method {
			case rpcMethodInitialize:
				initializations++
				reply := rpcReply{
					JSONRPC: rpcVersion,
					ID:      message.ID,
					Result:  json.RawMessage(validInitializeResultJSON),
				}
				body, err := json.Marshal(reply)
				require.NoError(t, err)
				return httpResponse(http.StatusOK, http.Header{
					"Content-Type":   []string{"application/json"},
					"Mcp-Session-Id": []string{fmt.Sprintf("session-%d", initializations)},
				}, body), nil
			case rpcMethodInitialized:
				return httpResponse(http.StatusAccepted, nil, nil), nil
			case rpcMethodToolsCall:
				body := "data: {\"jsonrpc\":\"2.0\",\"id\":\"server-ping\",\"method\":\"ping\",\"params\":{}}\n\n"
				return httpResponse(http.StatusOK, http.Header{
					"Content-Type": []string{"text/event-stream"},
				}, []byte(body)), nil
			case "":
				require.Equal(t, "session-1", req.Header.Get("Mcp-Session-Id"))
				return httpResponse(http.StatusNotFound, nil, []byte("expired")), nil
			default:
				return httpResponse(http.StatusBadRequest, nil, nil), nil
			}
		}),
	}
	caller, err := NewHTTPCaller(context.Background(), HTTPOptions{
		Endpoint:   "http://mcp.test/rpc",
		Client:     client,
		ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
	})
	require.NoError(t, err)

	_, err = caller.CallTool(context.Background(), CallRequest{Tool: "search", Payload: json.RawMessage(`{}`)})
	require.EqualError(t, err, "MCP server request response returned HTTP status 404")
	require.Equal(t, 2, initializations)
}

func TestHTTPCallerDoesNotRestartForUnrelatedHTTPFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
		status    int
	}{
		{name: "sessionless not found", status: http.StatusNotFound},
		{name: "session server error", sessionID: "session-1", status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			initializations := 0
			client := &http.Client{
				Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					message := decodeTestRPCMessage(t, req)
					switch message.Method {
					case rpcMethodInitialize:
						initializations++
						response := rpcReply{
							JSONRPC: "2.0",
							ID:      message.ID,
							Result:  json.RawMessage(validInitializeResultJSON),
						}
						body, err := json.Marshal(response)
						require.NoError(t, err)
						headers := http.Header{"Content-Type": []string{"application/json"}}
						if test.sessionID != "" {
							headers.Set("Mcp-Session-Id", test.sessionID)
						}
						return httpResponse(http.StatusOK, headers, body), nil
					case rpcMethodInitialized:
						return httpResponse(http.StatusAccepted, nil, nil), nil
					case rpcMethodToolsCall:
						return httpResponse(test.status, nil, nil), nil
					default:
						return httpResponse(http.StatusBadRequest, nil, nil), nil
					}
				}),
			}
			caller, err := NewHTTPCaller(context.Background(), HTTPOptions{
				Endpoint:   "http://mcp.test/rpc",
				Client:     client,
				ClientInfo: ClientInfo{Name: "operations-agent", Version: "2.4.1"},
			})
			require.NoError(t, err)

			_, err = caller.CallTool(context.Background(), CallRequest{Tool: "search", Payload: json.RawMessage(`{}`)})
			require.EqualError(t, err, fmt.Sprintf("mcp rpc status %d", test.status))
			require.Equal(t, 1, initializations)
		})
	}
}

// initializedHTTPClient completes initialization and returns toolResponse when
// the caller sends a tool request.
func initializedHTTPClient(toolResponse func() *http.Response) *http.Client {
	return &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var req rpcMessage
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, err
			}
			if req.Method == "" {
				return httpResponse(http.StatusAccepted, nil, nil), nil
			}
			switch req.Method {
			case rpcMethodInitialize:
				response := rpcReply{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(validInitializeResultJSON),
				}
				data, err := json.Marshal(response)
				if err != nil {
					return nil, err
				}
				return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
			case rpcMethodInitialized:
				return httpResponse(http.StatusAccepted, nil, nil), nil
			case rpcMethodToolsCall:
				return toolResponse(), nil
			default:
				return httpResponse(http.StatusBadRequest, nil, nil), nil
			}
		}),
	}
}

// notificationHTTPClient completes the initialize request and returns
// notificationResponse when the caller says initialization is complete.
func notificationHTTPClient(notificationResponse func() *http.Response) *http.Client {
	return &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, err
			}
			if req.Method == rpcMethodInitialized {
				return notificationResponse(), nil
			}
			response := rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(validInitializeResultJSON),
			}
			data, err := json.Marshal(response)
			if err != nil {
				return nil, err
			}
			return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, data), nil
		}),
	}
}
