// Package runtime keeps unfinished MCP calls out of completed tool history.
// The workflow saves original arguments and server state, accepts host input for
// one exact call, and performs the next network round through a tool activity.
package runtime

import (
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/mcp"
)

// AwaitMCPInput returns an unfinished execution outcome. The runtime validates
// and saves it before publishing host input; no final tool result exists yet.
func AwaitMCPInput(input *mcp.InputRequired) (*ToolExecutionResult, error) {
	pending, err := tooloperation.NewPendingInput(input)
	if err != nil {
		return nil, err
	}
	return Unfinished(pending)
}

// Unfinished validates and copies an accepted pending outcome. The caller
// receives an execution with no completed tool data, or a construction error.
func Unfinished(pending *api.PendingExecution) (*ToolExecutionResult, error) {
	if pending == nil {
		return nil, errors.New("unfinished execution requires a pending outcome")
	}
	if err := pending.Validate(); err != nil {
		return nil, err
	}
	copy := *pending
	return &ToolExecutionResult{mcpPending: &copy}, nil
}

// pendingHostInput returns copied questions for ordinary or Task input. Waiting
// for a Task has no host questions and remains inside activity collection.
func pendingHostInput(pending *api.PendingExecution) *mcp.InputRequired {
	if input, ok := pending.AsInput(); ok {
		return input
	}
	_, _, input, _ := pending.AsTaskInput()
	return input
}

// executionToolCallID checks the final-or-unfinished outcome and returns its
// runtime-assigned invocation ID for ordered parallel result collection.
func executionToolCallID(outcome *ToolExecutionResult) (string, error) {
	if outcome == nil {
		return "", errors.New("workflow step execution returned an empty outcome")
	}
	if outcome.mcpPending != nil {
		if outcome.ToolResult != nil || outcome.Clarification != nil || outcome.childSuspension != nil || outcome.mcpToolCallID == "" {
			return "", errors.New("unfinished MCP execution has an invalid outcome")
		}
		return outcome.mcpToolCallID, nil
	}
	if outcome.ToolResult == nil || outcome.ToolResult.ToolCallID == "" {
		return "", errors.New("workflow step execution is missing its tool result identity")
	}
	return outcome.ToolResult.ToolCallID, nil
}

// pendingMCPInput exposes host requests while keeping the opaque state in the
// trusted checkpoint. Empty Requests asks the host when to resume a state-only round.
func pendingMCPInput(call ToolCall, pending *api.PendingExecution) *api.PendingMCPInput {
	return &api.PendingMCPInput{ToolName: call.Name, ToolCallID: call.ToolCallID, Requests: pendingHostInput(pending).Requests}
}

// applyExecutionContinuation sends validated answers to the same unfinished call. It
// replaces its pending state on another round or installs its final tool result.
func (l *workflowLoop) applyExecutionContinuation(batch *stepBatch, pending *api.PendingMCPInput, response *api.MCPInputResponse) ([]checkpointPendingInput, error) {
	if response == nil || response.ToolCallID != pending.ToolCallID {
		return nil, errors.New("MCP response does not match the pending invocation")
	}
	for i := range batch.records {
		record := &batch.records[i]
		if record.call.ToolCallID != pending.ToolCallID {
			continue
		}
		if record.mcpPending == nil || !reflect.DeepEqual(pendingMCPInput(record.call, record.mcpPending), pending) {
			return nil, errors.New("MCP continuation does not match saved input")
		}
		if err := pendingHostInput(record.mcpPending).ValidateResponses(response.Responses); err != nil {
			return nil, err
		}
		call := cloneToolCall(record.call)
		if call.ExecutionSequence == ^uint64(0) {
			return nil, errors.New("saved execution sequence cannot be incremented")
		}
		call.ExecutionSequence++
		input := pendingHostInput(record.mcpPending)
		var continuation *api.ExecutionContinuation
		var err error
		if record.task != nil {
			continuation, err = tooloperation.NewTaskUpdate(record.task.TaskID, response.Responses)
		} else {
			continuation, err = tooloperation.NewInput(&mcp.CallContinuation{RequestState: input.RequestState, InputResponses: response.Responses})
		}
		if err != nil {
			return nil, err
		}
		call.ExecutionContinuation = continuation
		outcomes, timedOut, err := l.executeImmediateToolCalls([]ToolCall{call}, record.expectedChildren, map[string]*taskExecution{call.ToolCallID: record.task})
		batch.timedOut = batch.timedOut || timedOut
		if err != nil {
			return nil, err
		}
		if len(outcomes) != 1 {
			return nil, errors.New("MCP continuation did not return one outcome")
		}
		outcome := outcomes[0]
		id, err := executionToolCallID(outcome)
		if err != nil {
			return nil, err
		}
		if id != record.call.ToolCallID {
			return nil, errors.New("MCP continuation changed tool call identity")
		}
		record.call.ExecutionSequence = max(call.ExecutionSequence, outcome.executionSequence)
		record.task = outcome.task
		record.duration += outcome.duration
		record.mcpPending = outcome.mcpPending
		if record.mcpPending != nil {
			return []checkpointPendingInput{{MCP: pendingMCPInput(record.call, record.mcpPending)}}, nil
		}
		record.result = outcome.ToolResult
		record.clarification = outcome.Clarification
		record.resultPublished = outcome.resultPublished
		record.resultRecord = outcome.resultRecord
		record.resultRunID = l.input.RunID
		record.requiresResume = true
		items, err := toolClarificationAwaitItems(toolClarificationsFromRecords([]stepToolRecord{*record}))
		if err != nil {
			return nil, err
		}
		generated := make([]checkpointPendingInput, 0, len(items))
		for i := range items {
			item := items[i]
			if err := l.r.publishAwaitToolUses(l.wfCtx.Context(), l.input, l.base, l.turnID, item, i); err != nil {
				return nil, err
			}
			generated = append(generated, checkpointPendingInput{Await: &item, CallRunID: l.input.RunID})
		}
		return generated, nil
	}
	return nil, fmt.Errorf("MCP continuation references unknown tool call %q", pending.ToolCallID)
}

// validateCheckpointMCPInputs proves that every unfinished saved call has one
// matching host request and no materialized final result.
func validateCheckpointMCPInputs(checkpoint *workflowCheckpoint) error {
	records := make(map[string]checkpointToolRecord)
	for _, record := range checkpoint.Batch.Records {
		if record.MCPPending == nil {
			if record.Task != nil {
				return errors.New("saved task state requires unfinished task input")
			}
			continue
		}
		if record.Call.TextOnly || checkpoint.Context.TextOnly || checkpoint.Policy != nil && checkpoint.Policy.TextOnly {
			return errors.New("text-only checkpoint cannot contain MCP host input")
		}
		if record.Result != nil || record.ResultRecord != nil || record.ResultPublished || len(record.ResultJSON) != 0 || record.Clarification != nil || record.ChildSuspension != nil {
			return errors.New("unfinished MCP call has a completed result")
		}
		if err := record.MCPPending.Validate(); err != nil {
			return err
		}
		if pendingHostInput(record.MCPPending) == nil {
			return errors.New("task waiting cannot be saved as host input")
		}
		if err := validateTaskInputState(record.MCPPending, record.Task); err != nil {
			return err
		}
		if _, exists := records[record.Call.ToolCallID]; exists {
			return errors.New("duplicate unfinished MCP call")
		}
		original := false
		for _, call := range checkpoint.Batch.Calls {
			if call.ToolCallID == record.Call.ToolCallID && sameMCPInvocation(call, record.Call) {
				original = true
				break
			}
		}
		if !original {
			return errors.New("unfinished MCP call does not match original arguments and identity")
		}
		records[record.Call.ToolCallID] = record
	}
	for _, pending := range checkpoint.Pending {
		if pending.MCP == nil {
			continue
		}
		record, ok := records[pending.MCP.ToolCallID]
		if !ok || !reflect.DeepEqual(pending.MCP, pendingMCPInput(record.Call, record.MCPPending)) {
			return errors.New("pending MCP input does not match saved invocation")
		}
		delete(records, pending.MCP.ToolCallID)
	}
	if len(records) != 0 {
		return errors.New("unfinished MCP call has no pending host input")
	}
	return nil
}

// sameMCPInvocation compares the saved arguments and stable call identity.
// A successor run changes execution labels and run ownership, not the invocation.
func sameMCPInvocation(current, original ToolCall) bool {
	current.RunID, original.RunID = "", ""
	current.TurnID, original.TurnID = "", ""
	current.ParentToolCallID, original.ParentToolCallID = "", ""
	current.Labels, original.Labels = nil, nil
	current.ExecutionSequence, original.ExecutionSequence = 0, 0
	return reflect.DeepEqual(current, original)
}
