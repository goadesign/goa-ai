// This file checks that every generated MCP executor gives the agent the same
// next action for the same remote failure.

package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
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
		{"tool rejected call", mcp.NewToolExecutionError(mcp.CallResponse{Content: []string{"rejected"}}), planner.FailureDomainRejection, planner.RecoveryReplan},
		{"invalid arguments", &mcp.Error{Code: mcp.JSONRPCInvalidParams, Message: "bad arguments"}, planner.FailureInvalidCall, planner.RecoveryCorrectCall},
		{"missing method", &mcp.Error{Code: mcp.JSONRPCMethodNotFound, Message: "missing"}, planner.FailureInvalidCall, planner.RecoveryReplan},
		{"other protocol error", &mcp.Error{Code: mcp.JSONRPCInternalError, Message: "failed"}, planner.FailureInternal, planner.RecoveryFinish},
		{"unavailable", errors.New("offline"), planner.FailureUnavailable, planner.RecoveryReplan},
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
