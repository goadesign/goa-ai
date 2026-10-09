// These tests follow a tool request from local preparation to its HTTP
// dependency. Local failures cannot mean that a remote tool ran; failures after
// dispatch retain uncertainty unless the peer explicitly rejected the request.
package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPToolFailureRetainsDispatchEvidence(t *testing.T) {
	cases := []struct {
		name, meta, reply string
		cancel            bool
		transportError    error
		wantCalls         int
		wantInternal      bool
		wantUnknown       bool
		wantProtocol      bool
		wantSuccess       bool
	}{
		{name: "object progress token", meta: `"progressToken":{}`, wantInternal: true},
		{name: "null progress token", meta: `"progressToken":null`, wantInternal: true},
		{name: "fractional progress token", meta: `"progressToken":1.5`, wantInternal: true},
		{name: "cancelled before dispatch", cancel: true},
		{name: "HTTP dependency failed", transportError: io.ErrUnexpectedEOF, wantCalls: 1, wantUnknown: true},
		{name: "malformed remote response", reply: `{"jsonrpc":"2.0","id":"call-1","result":null}`, wantCalls: 1, wantUnknown: true},
		{name: "explicit protocol rejection", reply: `{"jsonrpc":"2.0","id":"call-1","error":{"code":-32602,"message":"rejected"}}`, wantCalls: 1, wantProtocol: true},
		{name: "string progress token", meta: `"progressToken":"updates"`, reply: `{"jsonrpc":"2.0","id":"call-1","result":{"resultType":"complete"}}`, wantCalls: 1, wantSuccess: true},
		{name: "empty string progress token", meta: `"progressToken":""`, reply: `{"jsonrpc":"2.0","id":"call-1","result":{"resultType":"complete"}}`, wantCalls: 1, wantSuccess: true},
		{name: "zero progress token", meta: `"progressToken":0`, reply: `{"jsonrpc":"2.0","id":"call-1","result":{"resultType":"complete"}}`, wantCalls: 1, wantSuccess: true},
		{name: "large integer progress token", meta: `"progressToken":9007199254740993`, reply: `{"jsonrpc":"2.0","id":"call-1","result":{"resultType":"complete"}}`, wantCalls: 1, wantSuccess: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			calls := 0
			transport := NewHTTPTransport(transportFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				require.NoError(t, request.Body.Close())
				if tc.transportError != nil {
					return nil, tc.transportError
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(tc.reply)),
				}, nil
			}), ClientInfo{}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
			body := `{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"write","arguments":{},"_meta":{` + tc.meta + `}}}`
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://resource.example/mcp", strings.NewReader(body))
			require.NoError(t, err)
			response, err := transport.Do(request)
			if response != nil {
				require.NoError(t, response.Body.Close())
			}
			if tc.wantSuccess {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, tc.wantCalls, calls)
			var internal *InternalError
			var unknown *OutcomeUnknownError
			var protocol *Error
			assert.Equal(t, tc.wantInternal, errors.As(err, &internal))
			assert.Equal(t, tc.wantUnknown, errors.As(err, &unknown))
			assert.Equal(t, tc.wantProtocol, errors.As(err, &protocol))
			if tc.cancel {
				assert.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}
