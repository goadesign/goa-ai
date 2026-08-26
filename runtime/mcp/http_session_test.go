// This file checks the HTTP session wrapper that adds MCP headers, converts
// event streams to one JSON response, answers server pings, and clears expired
// session state without repeating the failed request.

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHTTPSessionAppliesInitializationHeaders(t *testing.T) {
	t.Parallel()

	var requests []*http.Request
	next := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Clone(req.Context()))
		message := decodeTestRPCMessage(t, req)
		if message.Method == rpcMethodInitialized {
			return httpResponse(http.StatusAccepted, nil, nil), nil
		}
		response := rpcReply{
			JSONRPC: "2.0",
			ID:      message.ID,
			Result:  json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{}}`),
		}
		body, err := json.Marshal(response)
		require.NoError(t, err)
		return httpResponse(http.StatusOK, http.Header{
			"Content-Type":   []string{"application/json"},
			"Mcp-Session-Id": []string{"session-1"},
		}, body), nil
	})
	session := NewHTTPSession(next, "2025-06-18")

	session.Reset()
	response, err := session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	session.Begin()
	require.True(t, session.Initialized())
	response, err = session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	require.Len(t, requests, 2)
	for _, request := range requests {
		require.Equal(t, "application/json, text/event-stream", request.Header.Get("Accept"))
	}
	require.Empty(t, requests[0].Header.Get("MCP-Protocol-Version"))
	require.Empty(t, requests[0].Header.Get("Mcp-Session-Id"))
	require.Equal(t, "2025-06-18", requests[1].Header.Get("MCP-Protocol-Version"))
	require.Equal(t, "session-1", requests[1].Header.Get("Mcp-Session-Id"))
}

func TestHTTPSessionRejectsInvalidSessionID(t *testing.T) {
	t.Parallel()

	tests := []string{
		"contains space",
		"contains\tseparator",
		"contains\x7fdelete",
		"contains-unicode-é",
	}
	for _, sessionID := range tests {
		t.Run(sessionID, func(t *testing.T) {
			t.Parallel()

			next := httpDoerFunc(func(*http.Request) (*http.Response, error) {
				body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
				return httpResponse(http.StatusOK, http.Header{
					"Content-Type":   []string{"application/json"},
					"Mcp-Session-Id": []string{sessionID},
				}, body), nil
			})
			session := NewHTTPSession(next, "2025-06-18")

			response, err := session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
			if response != nil {
				t.Cleanup(func() {
					require.NoError(t, response.Body.Close())
				})
			}

			require.Nil(t, response)
			require.ErrorContains(t, err, "MCP session ID must contain only visible ASCII characters")
			require.False(t, session.Initialized())
		})
	}
}

func TestHTTPSessionClearsExpiredSessionWithoutRepeatingRequest(t *testing.T) {
	t.Parallel()

	requests := 0
	next := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		message := decodeTestRPCMessage(t, req)
		if message.Method == "initialize" {
			response := rpcReply{
				JSONRPC: "2.0",
				ID:      message.ID,
				Result:  json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{}}`),
			}
			body, err := json.Marshal(response)
			require.NoError(t, err)
			return httpResponse(http.StatusOK, http.Header{
				"Content-Type":   []string{"application/json"},
				"Mcp-Session-Id": []string{"expired-session"},
			}, body), nil
		}
		return httpResponse(http.StatusNotFound, http.Header{
			"Mcp-Session-Id": []string{"must-not-replace-expired-session"},
		}, []byte("expired")), nil
	})
	session := NewHTTPSession(next, "2025-06-18")
	session.Reset()
	response, err := session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	session.Begin()

	request := newTestRPCRequest(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{}}`)
	response, err = session.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.Equal(t, "must-not-replace-expired-session", response.Header.Get("Mcp-Session-Id"))
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "expired", string(responseBody))
	require.NoError(t, response.Body.Close())
	require.Equal(t, "expired-session", request.Header.Get("Mcp-Session-Id"))
	require.Equal(t, 2, requests)
	require.False(t, session.Initialized())

	initialize := newTestRPCRequest(t, `{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`)
	response, err = session.Do(initialize)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Empty(t, initialize.Header.Get("MCP-Protocol-Version"))
	require.Empty(t, initialize.Header.Get("Mcp-Session-Id"))
}

func TestHTTPSessionNormalizesEventStreamAndAnswersPings(t *testing.T) {
	t.Parallel()

	var replies []rpcMessage
	next := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		message := decodeTestRPCMessage(t, req)
		if message.Method == "initialize" {
			body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
			return httpResponse(http.StatusOK, http.Header{
				"Content-Type":   []string{"application/json"},
				"Mcp-Session-Id": []string{"session-1"},
			}, body), nil
		}
		if message.Method == "tools/call" {
			body := strings.Join([]string{
				"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}",
				"",
				"data: {\"jsonrpc\":\"2.0\",\"id\":\"ping-string\",\"method\":\"ping\",\"params\":{}}",
				"",
				"data: {\"jsonrpc\":\"2.0\",\"id\":73,\"method\":\"ping\",\"params\":{}}",
				"",
				"data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}",
				"",
			}, "\n")
			return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}}, []byte(body)), nil
		}
		replies = append(replies, message)
		return httpResponse(http.StatusAccepted, nil, nil), nil
	})
	session := NewHTTPSession(next, "2025-06-18")
	session.Reset()
	response, err := session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	session.Begin()

	response, err = session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{}}`))
	require.NoError(t, err)
	require.Equal(t, "application/json", response.Header.Get("Content-Type"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"done"}]}}`, string(body))
	require.Len(t, replies, 2)
	require.JSONEq(t, `"ping-string"`, string(replies[0].ID))
	require.JSONEq(t, `{}`, string(replies[0].Result))
	require.JSONEq(t, `73`, string(replies[1].ID))
	require.JSONEq(t, `{}`, string(replies[1].Result))
}

func TestHTTPSessionExpiresWhenServerRequestReplyIsRejected(t *testing.T) {
	t.Parallel()

	requests := 0
	next := httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		message := decodeTestRPCMessage(t, req)
		switch requests {
		case 1:
			reply := rpcReply{
				JSONRPC: "2.0",
				ID:      message.ID,
				Result:  json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{}}`),
			}
			body, err := json.Marshal(reply)
			require.NoError(t, err)
			return httpResponse(http.StatusOK, http.Header{
				"Content-Type":   []string{"application/json"},
				"Mcp-Session-Id": []string{"expired-session"},
			}, body), nil
		case 2:
			body := "data: {\"jsonrpc\":\"2.0\",\"id\":\"server-ping\",\"method\":\"ping\",\"params\":{}}\n\n"
			return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}}, []byte(body)), nil
		default:
			require.Equal(t, "expired-session", req.Header.Get("Mcp-Session-Id"))
			return httpResponse(http.StatusNotFound, nil, []byte("expired")), nil
		}
	})
	session := NewHTTPSession(next, "2025-06-18")
	session.Reset()
	response, err := session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	session.Begin()

	response, err = session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{}}`))
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.Nil(t, response)
	require.EqualError(t, err, "MCP server request response returned HTTP status 404")
	require.False(t, session.Initialized())
	require.Equal(t, 3, requests)
}

func TestHTTPSessionRejectsMalformedResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "invalid JSON", contentType: "application/json", body: `{"jsonrpc":`},
		{name: "wrong response ID", contentType: "application/json", body: `{"jsonrpc":"2.0","id":99,"result":{}}`},
		{name: "result and error", contentType: "application/json", body: `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32603,"message":"broken"}}`},
		{name: "invalid event data", contentType: "text/event-stream", body: "data: not-json\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			next := httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(http.StatusOK, http.Header{"Content-Type": []string{test.contentType}}, []byte(test.body)), nil
			})
			session := NewHTTPSession(next, "2025-06-18")
			response, err := session.Do(newTestRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`))
			if response != nil {
				require.NoError(t, response.Body.Close())
			}
			require.ErrorContains(t, err, "malformed MCP response")
		})
	}
}

// newTestRPCRequest builds the exact HTTP request that the session wrapper
// receives from a handwritten or generated MCP client.
func newTestRPCRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://mcp.test/rpc", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeTestRPCMessage reads one request sent through the session wrapper so
// tests can assert its headers and serialized JSON message.
func decodeTestRPCMessage(t *testing.T, req *http.Request) rpcMessage {
	t.Helper()
	var message rpcMessage
	require.NoError(t, json.NewDecoder(req.Body).Decode(&message))
	require.NoError(t, req.Body.Close())
	return message
}
