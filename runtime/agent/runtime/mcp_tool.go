// Package runtime performs every MCP-backed tool operation through the same
// caller and saved invocation. Generated code supplies the exact remote name
// and result specification. Only completed output is decoded; input, Task
// handles and acknowledgments return one immutable unfinished value.
package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
	"goa.design/goa-ai/runtime/toolregistry"
)

// ExecuteMCPTool performs the saved operation on one tool and decodes a completed
// reply with the supplied original specification. Task operations never repeat
// the creating tools/call. Delivery failures return activity errors; permanent
// rejection carries the engine's nonretryable marker. Observed terminal Task
// failures return completed tool failures without correcting original arguments.
func ExecuteMCPTool(ctx context.Context, caller mcp.Caller, call *ToolCall, remoteName string, spec tools.ToolSpec) (*ToolExecutionResult, error) {
	if err := toolregistry.ValidateExecution(call.ExecutionSequence, call.ExecutionContinuation, call.TextOnly); err != nil {
		return Executed(&planner.ToolResult{Name: call.Name, Failure: &planner.ToolFailure{
			Kind: planner.FailureInvalidCall, Error: planner.ToolErrorFromError(err), Recovery: planner.RecoveryDirective{Action: planner.RecoveryFinish},
		}}), nil
	}
	if call.TextOnly {
		ctx = mcp.WithoutHostInput(ctx)
	}

	// Existing Task operations use the same endpoint and credentials but have
	// separate saved sequence identities. An update acknowledgment is returned
	// before another observation is scheduled by the workflow.
	var response mcp.CallResponse
	var input *mcp.CallContinuation
	if continuation := call.ExecutionContinuation; continuation != nil {
		if taskID, ok := continuation.AsTaskGet(); ok {
			task, err := caller.GetTask(ctx, taskID)
			if err != nil {
				return nil, mcpTaskDeliveryError(call.ExecutionContinuation, err)
			}
			info := task.Info()
			if info.TaskID != taskID {
				return nil, engine.MarkActivityErrorNonRetryable(mcp.NewMalformedResponseError(errors.New("task observation changed its identifier")))
			}
			switch info.Status {
			case mcp.TaskWorking:
				return awaitMCPTask(taskID, info.PollIntervalMs)
			case mcp.TaskInputRequired:
				questions, ok := task.AsInputRequired()
				if !ok {
					return nil, engine.MarkActivityErrorNonRetryable(mcp.NewMalformedResponseError(errors.New("task input is missing questions")))
				}
				pending, err := api.NewPendingTaskInput(taskID, info.PollIntervalMs, questions)
				if err != nil {
					return nil, engine.MarkActivityErrorNonRetryable(mcp.NewMalformedResponseError(err))
				}
				return Unfinished(pending)
			case mcp.TaskCompleted:
				var ok bool
				response, ok = task.AsCompleted()
				if !ok {
					return nil, engine.MarkActivityErrorNonRetryable(mcp.NewMalformedResponseError(errors.New("completed Task is missing its result")))
				}
			case mcp.TaskFailed:
				failure, ok := task.AsFailed()
				if !ok {
					return nil, engine.MarkActivityErrorNonRetryable(mcp.NewMalformedResponseError(errors.New("failed Task is missing its protocol error")))
				}
				return Executed(mcpTaskFailure(call.Name, failure)), nil
			case mcp.TaskCancelled:
				return Executed(mcpTaskFailure(call.Name, context.Canceled)), nil
			default:
				return nil, engine.MarkActivityErrorNonRetryable(mcp.NewMalformedResponseError(errors.New("task observation has an unsupported status")))
			}
		} else if taskID, answers, ok := continuation.AsTaskUpdate(); ok {
			if err := caller.UpdateTask(ctx, taskID, answers); err != nil {
				return nil, mcpTaskDeliveryError(call.ExecutionContinuation, err)
			}
			return awaitMCPTask(taskID, nil)
		} else if taskID, ok := continuation.AsTaskCancel(); ok {
			if err := caller.CancelTask(ctx, taskID); err != nil {
				return nil, mcpTaskDeliveryError(call.ExecutionContinuation, err)
			}
			return awaitMCPTask(taskID, nil)
		} else {
			input, _ = continuation.AsInput()
		}
	}

	// The original invocation and ordinary input rounds use tools/call. A Task
	// handle leaves this activity before the workflow schedules its first read.
	if call.ExecutionContinuation == nil || input != nil {
		var err error
		response, err = caller.CallTool(ctx, mcp.CallRequest{Tool: remoteName, Payload: json.RawMessage(call.Payload), Continuation: input})
		if err != nil {
			return Executed(MCPCallFailure(call.Name, err)), nil
		}
		if response.Task != nil {
			if response.InputRequired != nil || len(response.StructuredContent) > 0 || len(response.Content) > 0 {
				return Executed(MCPCallFailure(call.Name, mcp.NewMalformedResponseError(errors.New("task creation cannot contain completed output or host input")))), nil
			}
			return awaitMCPTask(response.Task.TaskID, response.Task.PollIntervalMs)
		}
		if response.InputRequired != nil {
			return AwaitMCPInput(response.InputRequired)
		}
	}
	return completedMCPExecution(call.Name, spec, response), nil
}

// awaitMCPTask records the next job read without making that request in the
// same activity as creation or an update acknowledgment.
func awaitMCPTask(taskID string, pollIntervalMs *int64) (*ToolExecutionResult, error) {
	pending, err := api.NewPendingTaskWait(taskID, pollIntervalMs)
	if err != nil {
		return nil, err
	}
	return Unfinished(pending)
}

// completedMCPExecution checks content and the original typed result contract.
// A method without a result rejects structured output instead of discarding it.
func completedMCPExecution(name tools.Ident, spec tools.ToolSpec, response mcp.CallResponse) *ToolExecutionResult {
	if err := response.Content.Validate(); err != nil {
		return Executed(MCPCallFailure(name, mcp.NewMalformedResponseError(err)))
	}
	if response.IsError {
		return Executed(MCPCallFailure(name, mcp.NewToolExecutionError(response)))
	}
	var result any
	if spec.Result.Codec.FromJSON == nil {
		if len(response.StructuredContent) > 0 {
			return Executed(MCPCallFailure(name, mcp.NewMalformedResponseError(errors.New("MCP response for a method without a result must omit structured content"))))
		}
	} else {
		if len(response.StructuredContent) == 0 {
			return Executed(MCPCallFailure(name, mcp.NewMalformedResponseError(errors.New("MCP response is missing structured content"))))
		}
		value, err := spec.Result.Codec.FromJSON(response.StructuredContent)
		if err != nil {
			return Executed(MCPCallFailure(name, mcp.NewMalformedResponseError(err)))
		}
		result = value
	}
	return Executed(&planner.ToolResult{Name: name, Result: result, Blocks: response.Content.Clone()})
}

// mcpTaskFailure keeps a Task operation error terminal for this invocation.
// Invalid Task identifiers or methods cannot become corrections to the original
// tool arguments or requests to start the tool's effects again.
func mcpTaskFailure(name tools.Ident, err error) *planner.ToolResult {
	result := MCPCallFailure(name, err)
	result.Failure.Recovery.Action = planner.RecoveryFinish
	if result.Failure.Kind == planner.FailureInvalidCall {
		result.Failure.Kind = planner.FailureInternal
	}
	return result
}
