// These tests use independent HTTP responses to verify that stream loss retries
// only trusted, repeatable tools and never changes the original request inputs.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPInterruptedResponseRetry(t *testing.T) {
	tests := []struct {
		name          string
		binding       ToolBinding
		policy        HTTPRetryPolicy
		lost          int
		response      string
		cancel        bool
		status        int
		streamError   bool
		partialEvent  string
		protocolError bool
		wantCalls     int
		wantError     bool
	}{
		{name: "HTTP rejection with lost body", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, lost: 1, status: 400, wantCalls: 1, wantError: true},
		{name: "truncated event", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, lost: 1, partialEvent: `data: {"jsonrpc":"2.0","id":`, wantCalls: 2},
		{name: "unterminated event", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, lost: 1, partialEvent: "data: {}\n", wantCalls: 2},
		{name: "stream read error", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, lost: 1, streamError: true, wantCalls: 2},
		{name: "protocol rejection", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, protocolError: true, wantCalls: 1, wantError: true},
		{name: "trusted read-only", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, lost: 1, wantCalls: 2},
		{name: "trusted idempotent", binding: ToolBinding{Idempotent: true}, policy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}, lost: 2, wantCalls: 3},
		{name: "untrusted hint", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 3}, lost: 1, wantCalls: 1, wantError: true},
		{name: "missing safe hints", policy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}, lost: 1, wantCalls: 1, wantError: true},
		{name: "zero means one attempt", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{TrustToolAnnotations: true}, lost: 1, wantCalls: 1, wantError: true},
		{name: "one attempt", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 1, TrustToolAnnotations: true}, lost: 1, wantCalls: 1, wantError: true},
		{name: "exhausted attempts", binding: ToolBinding{Idempotent: true}, policy: HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true}, lost: 3, wantCalls: 2, wantError: true},
		{name: "cancellation", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}, lost: 1, cancel: true, wantCalls: 1, wantError: true},
		{name: "malformed message", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}, response: "data: broken\n\n", wantCalls: 1, wantError: true},
		{name: "completed tool error", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}, response: `{"resultType":"complete","content":[],"isError":true}`, wantCalls: 1, wantError: true},
		{name: "completed result", binding: ToolBinding{ReadOnly: true}, policy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var ids []string
			var inputs []json.RawMessage
			transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
				var message struct {
					ID     string
					Params json.RawMessage
				}
				require.NoError(t, json.NewDecoder(req.Body).Decode(&message))
				require.NoError(t, req.Body.Close())
				assert.Equal(t, "POST", req.Method)
				assert.Empty(t, req.Header.Get("Last-Event-ID"))
				assert.Nil(t, req.GetBody)
				ids = append(ids, message.ID)
				inputs = append(inputs, message.Params)
				body := ": accepted\n\n"
				if test.partialEvent != "" {
					body = test.partialEvent
				}
				if test.cancel {
					cancel()
				}
				if len(ids) > test.lost {
					result := test.response
					if result == "" {
						result = `{"resultType":"complete","content":[],"structuredContent":42}`
					}
					if strings.HasPrefix(result, "data:") {
						body = result
					} else {
						body = fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":%s}\n\n", message.ID, result)
					}
				}
				if test.protocolError {
					body = fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%q,\"error\":{\"code\":-32602,\"message\":\"rejected\"}}\n\n", message.ID)
				}
				status := test.status
				if status == 0 {
					status = 200
				}
				var reader io.Reader = strings.NewReader(body)
				if test.streamError && len(ids) <= test.lost {
					reader = io.MultiReader(reader, iotest.ErrReader(io.ErrUnexpectedEOF))
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(reader)}, nil
			}), ClientInfo{Name: "test", Version: "1"}, map[string]ToolBinding{"read": test.binding}, InputSupport{}, test.policy)
			response, err := transport.CallTool(ctx, "https://example.test/mcp", CallRequest{Tool: "read", Payload: json.RawMessage(`{"operation":"stable","value":1}`)})
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.JSONEq(t, `42`, string(response.StructuredContent))
			}
			assert.Len(t, ids, test.wantCalls)
			distinct := make(map[string]bool)
			for i, id := range ids {
				assert.NotEmpty(t, id)
				assert.False(t, distinct[id])
				distinct[id] = true
				assert.True(t, bytes.Equal(inputs[0], inputs[i]))
			}
		})
	}
}

func TestHTTPRetryLimitAppliesToEachInputRound(t *testing.T) {
	var ids []string
	var inputs []json.RawMessage
	transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
		var message struct {
			ID     string
			Params json.RawMessage
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&message))
		require.NoError(t, req.Body.Close())
		ids = append(ids, message.ID)
		inputs = append(inputs, message.Params)
		body := ": accepted\n\n"
		if len(ids)%2 == 0 {
			result := `{"resultType":"input_required","requestState":"opaque"}`
			if len(ids) == 4 {
				result = `{"resultType":"complete","content":[],"structuredContent":42}`
			}
			body = fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":%s}\n\n", message.ID, result)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}), ClientInfo{Name: "test", Version: "1"}, map[string]ToolBinding{"read": {ReadOnly: true}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 2, TrustToolAnnotations: true})
	request := CallRequest{Tool: "read", Payload: json.RawMessage(`{"value":1}`)}
	first, err := transport.CallTool(t.Context(), "https://example.test/mcp", request)
	require.NoError(t, err)
	require.NotNil(t, first.InputRequired)
	request.Continuation = &CallContinuation{RequestState: first.InputRequired.RequestState}
	last, err := transport.CallTool(t.Context(), "https://example.test/mcp", request)
	require.NoError(t, err)
	assert.JSONEq(t, `42`, string(last.StructuredContent))
	require.Len(t, inputs, 4)
	assert.True(t, bytes.Equal(inputs[0], inputs[1]))
	assert.True(t, bytes.Equal(inputs[2], inputs[3]))
	assert.NotEqual(t, inputs[0], inputs[2])
	assert.Contains(t, string(inputs[2]), `"requestState":"opaque"`)
	distinct := make(map[string]bool)
	for _, id := range ids {
		assert.False(t, distinct[id])
		distinct[id] = true
	}
}

func TestHTTPRetryPolicyRejectsNegativeAttempts(t *testing.T) {
	_, err := NewHTTPCaller(HTTPOptions{Endpoint: "https://example.test/mcp", ClientInfo: ClientInfo{Name: "test", Version: "1"}, RetryPolicy: HTTPRetryPolicy{MaxAttempts: -1}})
	assert.ErrorContains(t, err, "MaxAttempts must be non-negative")
}

func TestHTTPEventStreamFraming(t *testing.T) {
	message := `{"jsonrpc":"2.0","id":"reply","result":{"resultType":"complete"}}`
	for _, separator := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("separator %q", separator), func(t *testing.T) {
			stream := "\uFEFF: comment" + separator + separator + "data: " + message + separator + separator
			result, err := readEventStream(strings.NewReader(stream), nil)
			require.NoError(t, err)
			assert.JSONEq(t, message, string(result))
		})
	}
	result, err := readEventStream(strings.NewReader("data: {\ndata: \"jsonrpc\":\"2.0\",\"id\":\"reply\",\"result\":{\"resultType\":\"complete\"}}\n\n"), nil)
	require.NoError(t, err)
	assert.JSONEq(t, message, string(result))
	for _, empty := range []string{"data\n\n", "data:\n\n"} {
		_, err := readEventStream(strings.NewReader(empty), nil)
		var malformed *MalformedResponseError
		assert.ErrorAs(t, err, &malformed)
	}
}
