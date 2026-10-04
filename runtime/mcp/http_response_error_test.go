// These tests preserve the HTTP status and authorization challenges returned by
// independent peers. A rejected request cannot become an interrupted tool retry,
// and a challenge never requires reading an MCP envelope or an event stream.
package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	unreadChallengeBody struct {
		reads, closes int
		closeErr      error
	}
)

func TestHTTPAuthorizationRejectionDoesNotReadOrRetry(t *testing.T) {
	challenges := []string{
		`Bearer resource_metadata="https://example.test/.well-known/oauth-protected-resource", scope="records:write", error="insufficient_scope"`,
		`Basic realm="other"`,
	}
	for _, test := range []struct {
		name        string
		status      int
		contentType string
		closeErr    error
		noChallenge bool
	}{
		{name: "missing content type", status: http.StatusUnauthorized},
		{name: "unauthorized without challenge", status: http.StatusUnauthorized, noChallenge: true},
		{name: "forbidden without challenge", status: http.StatusForbidden, noChallenge: true},
		{name: "plain text", status: http.StatusUnauthorized, contentType: "text/plain"},
		{name: "event stream", status: http.StatusForbidden, contentType: "text/event-stream"},
		{name: "JSON without MCP envelope", status: http.StatusForbidden, contentType: "application/json"},
		{name: "malformed authorization", status: http.StatusBadRequest},
		{name: "body close failure", status: http.StatusUnauthorized, closeErr: io.ErrClosedPipe},
	} {
		for _, method := range []string{"tools/call", "server/discover", "notifications/example"} {
			t.Run(test.name+"/"+method, func(t *testing.T) {
				calls := 0
				body := &unreadChallengeBody{closeErr: test.closeErr}
				expectedChallenges := challenges
				if test.noChallenge {
					expectedChallenges = nil
				}
				headers := http.Header{"Www-Authenticate": append([]string(nil), expectedChallenges...)}
				if test.contentType != "" {
					headers.Set("Content-Type", test.contentType)
				}
				transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					assert.Equal(t, "Bearer test-access", req.Header.Get("Authorization"))
					require.NoError(t, req.Body.Close())
					return &http.Response{StatusCode: test.status, Header: headers, Body: body}, nil
				}), ClientInfo{}, map[string]ToolBinding{"lookup": {ReadOnly: true}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true})
				identifier := `"id":"call-1",`
				if strings.HasPrefix(method, "notifications/") {
					identifier = ""
				}
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0",`+identifier+`"method":"`+method+`","params":{"name":"lookup","arguments":{}}}`))
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer test-access")
				response, err := transport.Do(request)
				if response != nil {
					require.NoError(t, response.Body.Close())
				}
				var failure *HTTPResponseError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, test.status, failure.StatusCode)
				assert.Equal(t, expectedChallenges, failure.WWWAuthenticate)
				assert.Equal(t, 1, calls)
				assert.Zero(t, body.reads)
				assert.Equal(t, 1, body.closes)
				if test.closeErr != nil {
					require.ErrorIs(t, err, test.closeErr)
				}
				var unknown *OutcomeUnknownError
				assert.NotErrorAs(t, err, &unknown)
				var malformed *MalformedResponseError
				assert.NotErrorAs(t, err, &malformed)
				assert.NotContains(t, err.Error(), "records:write")
				assert.NotContains(t, err.Error(), "resource_metadata")
				assert.NotContains(t, err.Error(), "test-access")
				if len(expectedChallenges) != 0 {
					headers.Values("WWW-Authenticate")[0] = "changed"
					assert.Equal(t, expectedChallenges, failure.WWWAuthenticate)
				}
			})
		}
	}
}

func TestHTTPProtocolFailureRetainsStatusAndExactData(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusInternalServerError} {
		calls := 0
		transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			require.NoError(t, req.Body.Close())
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":"call-1","error":{"code":-32602,"message":"rejected","data":{"sequence":9007199254740993}}}`))}, nil
		}), ClientInfo{}, map[string]ToolBinding{"lookup": {Idempotent: true}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true})
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"lookup","arguments":{}}}`))
		require.NoError(t, err)
		result, err := transport.Do(request)
		if result != nil {
			require.NoError(t, result.Body.Close())
		}
		var response *HTTPResponseError
		require.ErrorAs(t, err, &response)
		assert.Equal(t, status, response.StatusCode)
		var protocol *Error
		require.ErrorAs(t, err, &protocol)
		assert.Equal(t, JSONRPCInvalidParams, protocol.Code)
		assert.JSONEq(t, `{"sequence":9007199254740993}`, string(protocol.Data))
		assert.Contains(t, string(protocol.Data), "9007199254740993")
		assert.Equal(t, "rejected", protocol.Message)
		assert.Equal(t, 1, calls)
		var unknown *OutcomeUnknownError
		assert.NotErrorAs(t, err, &unknown)
	}
}

func TestHTTPServerStreamFailureKeepsUnknownToolOutcome(t *testing.T) {
	calls := 0
	transport := NewHTTPTransport(transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		require.NoError(t, req.Body.Close())
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(": lost\n\n"))}, nil
	}), ClientInfo{}, map[string]ToolBinding{"lookup": {ReadOnly: true}}, InputSupport{}, HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true})
	_, err := transport.CallTool(t.Context(), "https://example.test/mcp", CallRequest{Tool: "lookup", Payload: json.RawMessage(`{}`)})
	var unknown *OutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	var response *HTTPResponseError
	require.ErrorAs(t, err, &response)
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	assert.Equal(t, 1, calls)
}

func TestHTTPCallerRetainsAuthorizationDuringCatalogAndExecution(t *testing.T) {
	for _, rejectedMethod := range []string{"tools/list", "tools/call"} {
		t.Run(rejectedMethod, func(t *testing.T) {
			var methods []string
			challenge := `Bearer error="insufficient_scope", scope="records:write"`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var message struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&message)) {
					return
				}
				methods = append(methods, message.Method)
				if message.Method == rejectedMethod {
					w.Header().Set("WWW-Authenticate", challenge)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if !assert.Equal(t, "tools/list", message.Method) {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Result  json.RawMessage `json:"result"`
				}{rpcVersion, message.ID, json.RawMessage(`{"resultType":"complete","ttlMs":0,"cacheScope":"private","tools":[{"name":"lookup","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}`)}))
			}))
			t.Cleanup(server.Close)
			caller, err := NewHTTPCaller(HTTPOptions{Endpoint: server.URL, Client: server.Client(), ClientInfo: ClientInfo{Name: "tests", Version: "1"}, RetryPolicy: HTTPRetryPolicy{MaxAttempts: 3, TrustToolAnnotations: true}})
			require.NoError(t, err)
			_, err = caller.CallTool(t.Context(), CallRequest{Tool: "lookup", Payload: json.RawMessage(`{}`)})
			var failure *HTTPResponseError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, http.StatusForbidden, failure.StatusCode)
			assert.Equal(t, []string{challenge}, failure.WWWAuthenticate)
			if rejectedMethod == "tools/list" {
				assert.Equal(t, []string{"tools/list"}, methods)
			} else {
				assert.Equal(t, []string{"tools/list", "tools/call"}, methods)
			}
			var unknown *OutcomeUnknownError
			assert.NotErrorAs(t, err, &unknown)
		})
	}
}

func (body *unreadChallengeBody) Read([]byte) (int, error) {
	body.reads++
	return 0, errors.New("challenge body must not be read")
}

func (body *unreadChallengeBody) Close() error {
	body.closes++
	return body.closeErr
}
