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
	cleanup := *l
	cleanup.wfCtx = l.wfCtx.Detached()
	execution := &toolBatchExec{
		r: l.r, activityName: l.reg.ExecuteToolActivity, toolActOptions: l.toolOpts,
		runID: l.input.RunID, agentID: l.reg.Definition.route.ID, sessionID: l.input.SessionID,
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
	children := make([]agentChildFutureInfo, 0, len(batch.records))
	for i := range batch.records {
		saved := &batch.records[i]
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
		contains, err := checkpointChildContainsRun(saved.call, saved.childSuspension, l.reg.Definition, request.RunID)
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

// finishToolBatches transfers the accepted schedule facts to step records before
// releasing active execution state. Calls interrupted without a result keep
// their original schedule instead of publishing a second schedule during cleanup.
func (l *workflowLoop) finishToolBatches(records []stepToolRecord) {
	for _, execution := range l.activeToolBatches {
		for i := range records {
			record := &records[i]
			state, scheduled := execution.batch.scheduleByID[record.call.ToolCallID]
			if !scheduled {
				continue
			}
			record.callRunID = state.callRunID
			record.scheduleRequired = !state.published
			record.scheduleQueue = state.queue
			record.expectedChildren = state.expectedChildren
			record.scheduleExact = true
		}
	}
	l.activeToolBatches = nil
}
