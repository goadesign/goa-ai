// Package runtime settles unfinished work before a workflow closes cancellation.
// Initial and resumed workflows retain the same accepted Task and child records.
// Tasks keep their original call identity; children use ordinary published continuations.
package runtime

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/run"
)

// errWorkCleanupFailure keeps failed delivery distinct from the run's accepted
// cancellation. Both engine adapters then report failure even when the delivery
// itself returned a cancellation error.
var errWorkCleanupFailure = errors.New("failed to cancel unfinished work")

// cancelSavedWork sends one cancellation operation for each accepted Task and
// restores every suspended child for cleanup. A selected descendant retains the
// caller's reason; related cancellation uses the existing engine reason.
func (l *workflowLoop) cancelSavedWork(request api.CancellationRequest) error {
	batch := l.unfinishedBatch
	pending := l.unfinishedPending
	cleanup := *l
	cleanup.wfCtx = l.wfCtx.Detached()
	execution := &toolBatchExec{
		r: l.r, activityName: l.reg.ExecuteToolActivity, toolActOptions: l.toolOpts,
		runID: l.input.RunID, agentID: l.input.AgentID, sessionID: l.input.SessionID,
		turnID: l.turnID, runCtx: &l.base.RunContext, historyEndID: l.base.HistoryEndID,
		ownedTasks: make(map[string]futureInfo),
	}
	for _, record := range batch.records {
		if record.task == nil {
			continue
		}
		execution.ownedTasks[record.call.ToolCallID] = futureInfo{
			call: cloneToolCall(record.call), task: cloneTaskExecution(record.task),
		}
	}
	cleanupErr := execution.cancelAcceptedTasks(cleanup.wfCtx)
	children := make([]agentChildFutureInfo, 0, len(pending))
	for _, item := range pending {
		if item.Child == nil {
			continue
		}
		// A child already started by this continuation has its own joined
		// cancellation. Restore only children still owned by a saved record.
		var saved *stepToolRecord
		for i := range batch.records {
			if batch.records[i].call.ToolCallID == item.Child.ToolCallID {
				saved = &batch.records[i]
				break
			}
		}
		if saved.childSuspension == nil {
			continue
		}
		child, err := decodeWorkflowCheckpointState(saved.childSuspension)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		selected := api.CancellationRequest{RunID: child.PreviousRunID, Reason: run.CancellationReasonEngineCanceled}
		if request.Reason == run.CancellationReasonSessionEnded {
			selected.Reason = request.Reason
		}
		definition, err := childDefinitionForCall(saved.call, l.reg.Definition)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		contains, err := checkpointContainsRun(child, definition, request.RunID)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if contains {
			selected = request
		}
		info, _, err := cleanup.startChildContinuation(batch, &checkpointChildContinuation{ToolCallID: saved.call.ToolCallID, Suspension: saved.childSuspension}, &api.RunContinuationInput{Cancellation: &selected})
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("start suspended child cancellation: %w", err))
			continue
		}
		children = append(children, info)
	}
	for _, child := range children {
		_, err := awaitAgentChild(cleanup.wfCtx, child.handle, cleanup.wfCtx.Context())
		if err != nil && !temporalerrors.CancellationOnly(err) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("settle suspended child cancellation: %w", err))
		}
	}
	if cleanupErr != nil {
		return errors.Join(errWorkCleanupFailure, cleanupErr)
	}
	return nil
}
