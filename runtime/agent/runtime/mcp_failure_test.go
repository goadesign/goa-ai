// This file checks that every generated MCP executor gives the agent the same
// next action for the same remote failure.

package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"

	toolcontent "goa.design/goa-ai/runtime/content"
)

func TestMCPCallFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		kind   planner.FailureKind
		action planner.RecoveryAction
	}{
		{"deadline", context.DeadlineExceeded, planner.FailureTimeout, planner.RecoveryFinish},
		{"malformed response", mcp.NewMalformedResponseError(errors.New("bad result")), planner.FailureMalformedResult, planner.RecoveryFinish},
		{"client bug", mcp.NewInternalError(errors.New("bad state")), planner.FailureInternal, planner.RecoveryFinish},
		{"tool rejected call", mcp.NewToolExecutionError(mcp.CallResponse{Content: toolcontent.Blocks{&toolcontent.TextContent{Text: "rejected"}}}), planner.FailureDomainRejection, planner.RecoveryReplan},
		{"invalid arguments", &mcp.Error{Code: mcp.JSONRPCInvalidParams, Message: "bad arguments"}, planner.FailureInvalidCall, planner.RecoveryCorrectCall},
		{"missing method", &mcp.Error{Code: mcp.JSONRPCMethodNotFound, Message: "missing"}, planner.FailureInvalidCall, planner.RecoveryReplan},
		{"other protocol error", &mcp.Error{Code: mcp.JSONRPCInternalError, Message: "failed"}, planner.FailureInternal, planner.RecoveryFinish},
		{"unavailable", errors.New("offline"), planner.FailureUnavailable, planner.RecoveryFinish},
		{"unknown deadline", mcp.NewOutcomeUnknownError(context.DeadlineExceeded), planner.FailureTimeout, planner.RecoveryFinish},
		{"unknown outcome", mcp.NewOutcomeUnknownError(errors.New("lost reply")), planner.FailureUnavailable, planner.RecoveryFinish},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := MCPCallFailure(tools.Ident("remote.echo"), test.err)
			require.Equal(t, tools.Ident("remote.echo"), result.Name)
			require.Equal(t, test.kind, result.Failure.Kind)
			require.Equal(t, test.action, result.Failure.Recovery.Action)
			require.Equal(t, test.err.Error(), result.Failure.Error.Message)
			require.Empty(t, result.Failure.Recovery.PriorInput)
			require.Empty(t, result.Failure.Recovery.ExampleJSON)
		})
	}
}

// TestMCPHTTPFailureUsesDispatchEvidence follows transport errors into agent
// recovery. Local preparation bugs and remote uncertainty both stop execution,
// but only a dispatched request can leave the remote tool's outcome unknown.
func TestMCPHTTPFailureUsesDispatchEvidence(t *testing.T) {
	cases := []struct {
		name, meta  string
		cancel      bool
		wantCalls   int32
		wantKind    planner.FailureKind
		wantUnknown bool
	}{
		{name: "local progress token", meta: `"progressToken":{}`, wantKind: planner.FailureInternal},
		{name: "cancelled before dispatch", cancel: true, wantKind: planner.FailureUnavailable},
		{name: "invalid remote result", wantCalls: 1, wantKind: planner.FailureUnavailable, wantUnknown: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, err := writer.Write([]byte(`{"jsonrpc":"2.0","id":"call-1","result":null}`))
				if err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(peer.Close)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			transport := mcp.NewHTTPTransport(peer.Client(), mcp.ClientInfo{}, mcp.HTTPBindings{}, mcp.InputSupport{}, mcp.HTTPRetryPolicy{})
			body := `{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"write","arguments":{},"_meta":{` + tc.meta + `}}}`
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.URL, strings.NewReader(body))
			require.NoError(t, err)
			response, err := transport.Do(request)
			if response != nil {
				require.NoError(t, response.Body.Close())
			}
			require.Error(t, err)
			require.Equal(t, tc.wantCalls, calls.Load())
			var unknown *mcp.OutcomeUnknownError
			require.Equal(t, tc.wantUnknown, errors.As(err, &unknown))
			result := MCPCallFailure(tools.Ident("remote.write"), err)
			require.Equal(t, tc.wantKind, result.Failure.Kind)
			require.Equal(t, planner.RecoveryFinish, result.Failure.Recovery.Action)
			require.Empty(t, result.Failure.Recovery.PriorInput)
			require.Empty(t, result.Failure.Recovery.ExampleJSON)
		})
	}
}
