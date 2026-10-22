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

// checkpointInputConsumers joins saved call copies and identifies the inputs
// still read by execution, result materialization, or paging after restoration.
func checkpointInputConsumers(checkpoint *workflowCheckpoint) (*checkpointInputUses, error) {
	uses := checkpointInputUses{byID: make(map[string]int), current: make(map[string]bool)}
	events := make(map[string]*api.ToolEvent, len(checkpoint.State.ToolEvents))
	outputs := make(map[string]*planner.ToolOutput, len(checkpoint.State.ToolOutputs))
	for _, event := range checkpoint.State.ToolEvents {
		if _, exists := events[event.ToolCallID]; exists {
			return nil, fmt.Errorf("run suspension has duplicate result for call %q", event.ToolCallID)
		}
		events[event.ToolCallID] = event
	}
	for _, output := range checkpoint.State.ToolOutputs {
		outputs[output.ToolCallID] = output
		event, ok := events[output.ToolCallID]
		if !ok || event.Name != output.Name ||
			!reflect.DeepEqual(event.Failure, output.Failure) ||
			!bytes.Equal(event.Result, output.Result) ||
			!bytes.Equal(event.ServerData, output.ServerData) ||
			!reflect.DeepEqual(event.Bounds, output.Bounds) ||
			!reflect.DeepEqual(event.Blocks, output.Blocks) {
			return nil, fmt.Errorf("saved output does not match recorded result for call %q", output.ToolCallID)
		}
		if err := uses.add(checkpointOutputCall(output), false); err != nil {
			return nil, err
		}
	}
	historical := make(map[string]bool, len(checkpoint.Batch.Records))
	for i, record := range checkpoint.Batch.Records {
		id := record.Call.ToolCallID
		if _, exists := historical[id]; exists {
			return nil, fmt.Errorf("run suspension has duplicate saved tool call id %q", id)
		}
		complete := i < checkpoint.Batch.Recorded
		if event, exists := events[id]; exists && !reflect.DeepEqual(event, record.Result) {
			return nil, fmt.Errorf("saved batch result disagrees with recorded result for call %q", id)
		}
		if complete {
			if record.ChildSuspension != nil || !record.ResultPublished || record.ScheduleRequired ||
				record.CallRunID == "" || record.ResultRunID == "" ||
				!reflect.DeepEqual(events[id], record.Result) {
				return nil, fmt.Errorf("recorded batch call %q has unfinished or inconsistent result publication", id)
			}
			if output, ok := outputs[id]; ok &&
				(output.CallRunID != record.CallRunID || output.ResultRunID != record.ResultRunID) {
				return nil, fmt.Errorf("recorded batch call %q has inconsistent result provenance", id)
			}
		}
		// A validated materialized event already owns its preview. Publication
		// retries and delayed transcript append consume that event, not Args.
		// Failed outcomes skip typed success materialization and previews.
		// Recovery gives the planner raw evidence, not another execution.
		failed := record.Result != nil && record.Result.Failure != nil
		historical[id] = record.ChildSuspension == nil && record.MCPPending == nil &&
			(complete || record.ResultRecord != nil || failed)
		if err := uses.add(record.Call, !historical[id]); err != nil {
			return nil, err
		}
	}
	// Calls without recorded outcomes include unfinished children and lost
	// replies. They cannot borrow a successful sibling's historical status.
	for _, call := range checkpoint.Batch.Calls {
		if err := uses.add(call, !historical[call.ToolCallID]); err != nil {
			return nil, err
		}
	}
	for _, call := range awaitToolRequests(checkpoint.Batch.AwaitItems) {
		if err := uses.add(call, !historical[call.ToolCallID]); err != nil {
			return nil, err
		}
	}
	for _, pending := range checkpoint.Pending {
		if pending.Confirmation != nil {
			if err := uses.add(pending.Confirmation.Call, true); err != nil {
				return nil, err
			}
		}
		if pending.Await != nil {
			for _, call := range awaitToolRequests([]planner.AwaitItem{*pending.Await}) {
				if err := uses.add(call, true); err != nil {
					return nil, err
				}
			}
		}
	}
	// Accumulated outputs and already materialized batch records can close a
	// query. An unfinished result cannot retire an earlier accepted cursor.
	pagingOutputs := append([]*planner.ToolOutput(nil), checkpoint.State.ToolOutputs...)
	for i, record := range checkpoint.Batch.Records {
		if i < checkpoint.Batch.Recorded || record.ResultRecord == nil || record.ChildSuspension != nil || record.MCPPending != nil {
			continue
		}
		call, result := record.Call, record.Result
		if result == nil {
			return nil, fmt.Errorf("materialized tool call %q has no saved result", call.ToolCallID)
		}
		pagingOutputs = append(pagingOutputs, &planner.ToolOutput{
			Name: call.Name, ToolCallID: call.ToolCallID, Registry: call.Registry,
			ContinuationRootToolCallID: call.ContinuationRootToolCallID,
			Bounds:                     result.Bounds, Failure: result.Failure,
		})
	}
	chains, _, err := historicalPagingChains(pagingOutputs)
	if err != nil {
		return nil, err
	}
	for _, chain := range chains {
		head := chain[len(chain)-1]
		if head.Bounds == nil || head.Bounds.NextCursor == nil {
			continue
		}
		for _, output := range chain {
			uses.current[output.ToolCallID] = true
		}
	}
	// Recovery selects exact recorded failures. Its saved copy intentionally
	// omits paging roots and run-log references; compare only retained facts.
	selected, err := selectRecoveryOutputs(checkpoint.State.ToolOutputs, recoveryToolCallIDs(checkpoint.State.PendingRecovery))
	if err != nil {
		return nil, err
	}
	for i, output := range checkpoint.State.PendingRecovery {
		previous := selected[i]
		if previous.Name != output.Name || !bytes.Equal(previous.Payload, output.Payload) ||
			previous.ModelToolCallID != output.ModelToolCallID ||
			!reflect.DeepEqual(previous.Registry, output.Registry) ||
			!reflect.DeepEqual(previous.Failure, output.Failure) {
			return nil, fmt.Errorf("saved recovery input or failure disagrees for call %q", output.ToolCallID)
		}
	}
	return &uses, nil
}

// validateCheckpointInputs preserves completed inputs while checking every
// call that still supplies execution, materialization, or paging arguments.
func validateCheckpointInputs(checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	uses, err := checkpointInputConsumers(checkpoint)
	if err != nil {
		return err
	}
	for _, call := range uses.calls {
		if !uses.current[call.ToolCallID] {
			continue
		}
		if err := validateCheckpointToolRequest(call, definition); err != nil {
			return err
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
