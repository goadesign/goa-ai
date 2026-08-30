// This file gives every MCP-backed tool executor the same agent-visible
// failure and next action for errors returned by an MCP caller.

package runtime

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

// MCPCallFailure turns an MCP caller error into the failure recorded for the
// named tool. The runtime later adds the retained model input and registered
// example when invalid arguments allow the model to correct its call.
func MCPCallFailure(name tools.Ident, err error) *planner.ToolResult {
	kind := planner.FailureUnavailable
	action := planner.RecoveryReplan
	if errors.Is(err, context.DeadlineExceeded) {
		kind = planner.FailureTimeout
		action = planner.RecoveryFinish
	} else {
		var malformed *mcp.MalformedResponseError
		var internal *mcp.InternalError
		var execution *mcp.ToolExecutionError
		var rpcErr *mcp.Error
		switch {
		case errors.As(err, &malformed):
			kind = planner.FailureMalformedResult
			action = planner.RecoveryFinish
		case errors.As(err, &internal):
			kind = planner.FailureInternal
			action = planner.RecoveryFinish
		case errors.As(err, &execution):
			kind = planner.FailureDomainRejection
		case errors.As(err, &rpcErr):
			switch rpcErr.Code {
			case mcp.JSONRPCInvalidParams:
				kind = planner.FailureInvalidCall
				action = planner.RecoveryCorrectCall
			case mcp.JSONRPCMethodNotFound:
				kind = planner.FailureInvalidCall
			default:
				kind = planner.FailureInternal
				action = planner.RecoveryFinish
			}
		}
	}
	return &planner.ToolResult{
		Name: name,
		Failure: &planner.ToolFailure{
			Kind:  kind,
			Error: planner.ToolErrorFromError(err),
			Recovery: planner.RecoveryDirective{
				Action: action,
			},
		},
	}
}
