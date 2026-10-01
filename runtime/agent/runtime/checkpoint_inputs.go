package runtime

// Checkpoint inputs can be immutable history or arguments that current code
// still needs to interpret. Exact call identities and recorded batch progress
// distinguish these uses; a successful result alone does not establish history.

import (
	"bytes"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
)

// checkpointInputUses joins repeated saved inputs without changing their bytes.
// A current consumer in any representation requires validation for the call.
type checkpointInputUses struct {
	calls   []ToolCall
	byID    map[string]int
	current map[string]bool
}

// validateCheckpointInputs keeps accepted historical arguments intact. Pending
// execution, result publication, recovery and paging still need current inputs.
func validateCheckpointInputs(checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	uses := checkpointInputUses{byID: make(map[string]int), current: make(map[string]bool)}
	events := make(map[string]*api.ToolEvent, len(checkpoint.State.ToolEvents))
	outputs := make(map[string]*planner.ToolOutput, len(checkpoint.State.ToolOutputs))
	for _, event := range checkpoint.State.ToolEvents {
		if _, exists := events[event.ToolCallID]; exists {
			return fmt.Errorf("run suspension has duplicate result for call %q", event.ToolCallID)
		}
		events[event.ToolCallID] = event
	}
	for _, output := range checkpoint.State.ToolOutputs {
		outputs[output.ToolCallID] = output
		if output.Failure == nil {
			event, ok := events[output.ToolCallID]
			if !ok || event.Name != output.Name || event.Failure != nil ||
				!bytes.Equal(event.Result, output.Result) ||
				!bytes.Equal(event.ServerData, output.ServerData) ||
				!reflect.DeepEqual(event.Bounds, output.Bounds) {
				return fmt.Errorf("saved output does not match recorded result for call %q", output.ToolCallID)
			}
		}
		if err := uses.add(checkpointOutputCall(output), output.Failure != nil); err != nil {
			return err
		}
	}
	recorded := make(map[string]bool, len(checkpoint.Batch.Records))
	for i, record := range checkpoint.Batch.Records {
		id := record.Call.ToolCallID
		if _, exists := recorded[id]; exists {
			return fmt.Errorf("run suspension has duplicate saved tool call id %q", id)
		}
		complete := i < checkpoint.Batch.Recorded
		if complete {
			if record.ChildSuspension != nil || !record.ResultPublished || record.ScheduleRequired ||
				record.CallRunID == "" || record.ResultRunID == "" ||
				!reflect.DeepEqual(events[id], record.Result) {
				return fmt.Errorf("recorded batch call %q has unfinished or inconsistent result publication", id)
			}
			if output, ok := outputs[id]; ok &&
				(output.CallRunID != record.CallRunID || output.ResultRunID != record.ResultRunID) {
				return fmt.Errorf("recorded batch call %q has inconsistent result provenance", id)
			}
		}
		recorded[id] = complete
		if err := uses.add(record.Call, !complete || record.Result.Failure != nil); err != nil {
			return err
		}
	}
	// Calls without recorded outcomes include unfinished children and lost
	// replies. They cannot borrow a successful sibling's historical status.
	for _, call := range checkpoint.Batch.Calls {
		if err := uses.add(call, !recorded[call.ToolCallID]); err != nil {
			return err
		}
	}
	for _, call := range awaitToolRequests(checkpoint.Batch.AwaitItems) {
		if err := uses.add(call, !recorded[call.ToolCallID]); err != nil {
			return err
		}
	}
	for _, pending := range checkpoint.Pending {
		if pending.Confirmation != nil {
			if err := uses.add(pending.Confirmation.Call, true); err != nil {
				return err
			}
		}
		if pending.Await != nil {
			for _, call := range awaitToolRequests([]planner.AwaitItem{*pending.Await}) {
				if err := uses.add(call, true); err != nil {
					return err
				}
			}
		}
	}
	// Recovery outputs intentionally omit paging roots and run-log references.
	// Compare the input facts they actually retain, then require the codec.
	for _, output := range checkpoint.State.PendingRecovery {
		call := checkpointOutputCall(output)
		if index, exists := uses.byID[call.ToolCallID]; exists {
			previous := uses.calls[index]
			if previous.Name != call.Name || !bytes.Equal(previous.Payload, call.Payload) ||
				previous.ModelToolCallID != call.ModelToolCallID ||
				!reflect.DeepEqual(previous.Registry, call.Registry) {
				return fmt.Errorf("saved recovery input disagrees for call %q", call.ToolCallID)
			}
			uses.current[call.ToolCallID] = true
		} else if err := uses.add(call, true); err != nil {
			return err
		}
	}
	for _, call := range uses.calls {
		if call.Registry != nil {
			if _, err := validateRegistrySource(definition, call); err != nil {
				return err
			}
		}
		spec, ok, err := lookupCallSpec(call, definition.spec)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("tool %q is not in the current agent definition", call.Name)
		}
		// Paging reconstructs queries and consumes cursors from saved inputs,
		// including earlier pages of a chain that has since been exhausted.
		if uses.current[call.ToolCallID] || (spec.Bounds != nil && spec.Bounds.Paging != nil) {
			if err := validateCheckpointToolRequest(call, definition); err != nil {
				return err
			}
		}
	}
	return nil
}

// add requires all saved copies of a call to agree on the accepted arguments,
// selected registry contract and execution/provider/paging correlation. Run
// metadata is deliberately excluded: continuation workflows retarget it.
func (u *checkpointInputUses) add(call ToolCall, current bool) error {
	if call.ToolCallID == "" {
		return fmt.Errorf("saved tool input requires a call ID")
	}
	if index, exists := u.byID[call.ToolCallID]; exists {
		previous := u.calls[index]
		if previous.Name != call.Name || !bytes.Equal(previous.Payload, call.Payload) ||
			previous.ModelToolCallID != call.ModelToolCallID ||
			previous.ContinuationRootToolCallID != call.ContinuationRootToolCallID ||
			!reflect.DeepEqual(previous.Registry, call.Registry) {
			return fmt.Errorf("saved tool inputs disagree for call %q", call.ToolCallID)
		}
	} else {
		u.byID[call.ToolCallID] = len(u.calls)
		u.calls = append(u.calls, call)
	}
	u.current[call.ToolCallID] = u.current[call.ToolCallID] || current
	return nil
}

// checkpointOutputCall projects only the immutable identity and accepted input.
func checkpointOutputCall(output *planner.ToolOutput) ToolCall {
	return ToolCall{
		Name: output.Name, ToolCallID: output.ToolCallID,
		ModelToolCallID: output.ModelToolCallID, ContinuationRootToolCallID: output.ContinuationRootToolCallID,
		Payload: output.Payload, Registry: output.Registry,
	}
}
