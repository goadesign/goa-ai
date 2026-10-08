// Package runtime keeps unfinished MCP calls out of completed tool history.
// The workflow saves original arguments and server state, accepts host input for
// one exact call, and performs the next network round through a tool activity.
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/mcp"
)

// AwaitMCPInput returns an unfinished execution outcome. The runtime validates
// and saves it before publishing host input; no final tool result exists yet.
func AwaitMCPInput(input *mcp.InputRequired) *ToolExecutionResult {
	return &ToolExecutionResult{mcpInput: cloneMCPInput(input)}
}

// executionToolCallID checks the final-or-unfinished outcome and returns its
// runtime-assigned invocation ID for ordered parallel result collection.
func executionToolCallID(outcome *ToolExecutionResult) (string, error) {
	if outcome == nil {
		return "", errors.New("workflow step execution returned an empty outcome")
	}
	if outcome.mcpInput != nil {
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
func pendingMCPInput(call ToolCall, input *mcp.InputRequired) *api.PendingMCPInput {
	return &api.PendingMCPInput{ToolName: call.Name, ToolCallID: call.ToolCallID, Requests: cloneMCPInput(input).Requests}
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
		if record.mcpInput == nil || !reflect.DeepEqual(pendingMCPInput(record.call, record.mcpInput), pending) {
			return nil, errors.New("MCP continuation does not match saved input")
		}
		if err := record.mcpInput.ValidateResponses(response.Responses); err != nil {
			return nil, err
		}
		call := cloneToolCall(record.call)
		if call.ExecutionSequence == ^uint64(0) {
			return nil, errors.New("saved execution sequence cannot be incremented")
		}
		call.ExecutionSequence++
		input := cloneMCPInput(record.mcpInput)
		continuation, err := tooloperation.NewInput(&mcp.CallContinuation{RequestState: input.RequestState, InputResponses: response.Responses})
		if err != nil {
			return nil, err
		}
		call.ExecutionContinuation = continuation
		outcomes, timedOut, err := l.executeImmediateToolCalls([]ToolCall{call}, record.expectedChildren)
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
		record.call.ExecutionSequence = call.ExecutionSequence
		record.duration += outcome.duration
		record.mcpInput = outcome.mcpInput
		if record.mcpInput != nil {
			return []checkpointPendingInput{{MCP: pendingMCPInput(record.call, record.mcpInput)}}, nil
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
		if record.MCPInput == nil {
			continue
		}
		if record.Call.TextOnly || checkpoint.Context.TextOnly || checkpoint.Policy != nil && checkpoint.Policy.TextOnly {
			return errors.New("text-only checkpoint cannot contain MCP host input")
		}
		if record.Result != nil || record.ResultRecord != nil || record.ResultPublished || len(record.ResultJSON) != 0 || record.Clarification != nil || record.ChildSuspension != nil {
			return errors.New("unfinished MCP call has a completed result")
		}
		if err := record.MCPInput.Validate(mcp.InputSupport{Form: true, URL: true}); err != nil {
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
		if !ok || !reflect.DeepEqual(pending.MCP, pendingMCPInput(record.Call, record.MCPInput)) {
			return errors.New("pending MCP input does not match saved invocation")
		}
		delete(records, pending.MCP.ToolCallID)
	}
	if len(records) != 0 {
		return errors.New("unfinished MCP call has no pending host input")
	}
	return nil
}

func cloneMCPInput(input *mcp.InputRequired) *mcp.InputRequired {
	if input == nil {
		return nil
	}
	copy := *input
	if input.RequestState != nil {
		state := *input.RequestState
		copy.RequestState = &state
	}
	if input.Requests != nil {
		copy.Requests = make(map[string]mcp.InputRequest, len(input.Requests))
		for id, request := range input.Requests {
			request.Params = append(json.RawMessage(nil), request.Params...)
			copy.Requests[id] = request
		}
	}
	return &copy
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
