package runtime

// workflow_turn.go contains the tool-execution portion of one workflow step.
//
// Contract:
// - The function in this file is replay-safe: it uses workflow time and publishes
//   hook events deterministically based on inputs.
// - It owns the mechanics of taking planner ToolCalls through policy,
//   confirmation splitting, canonical assistant tool_use recording, and tool
//   execution.
// - It may also return await work when the planner result or executed tools
//   require an external input handshake before the step can transition.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
)

type stepTransition uint8

const (
	stepTransitionResume stepTransition = iota
	stepTransitionFinishCurrent
	stepTransitionFinishTerminal
)

// executeToolStep executes the immediate tool portion of one workflow step. It
// records produced tool results through the canonical result recorder and
// returns any await work that must be drained before the step can advance.
func (l *workflowLoop) executeToolStep(program stepProgram, batch *stepBatch) ([]confirmationAwait, []planner.AwaitItem, error) {
	ctx := l.wfCtx.Context()
	result := program.result
	candidates := program.calls
	l.r.logger.Info(ctx, "Workflow received tool calls from planner", "count", len(candidates))
	var err error
	candidates, err = l.r.rewriteUnknownToolCalls(candidates)
	if err != nil {
		return nil, nil, err
	}
	candidates, err = l.r.applyPerRunOverrides(ctx, l.input, candidates)
	if err != nil {
		return nil, nil, err
	}
	allowed, nextCaps, err := l.r.applyRuntimePolicy(ctx, l.base, l.input, candidates, l.st.Caps, l.turnID, result.RetryHint)
	if err != nil {
		return nil, nil, err
	}
	l.st.Caps = nextCaps
	if len(allowed) == 0 {
		l.r.logger.Error(ctx, "ERROR - No tools allowed for execution after filtering", "candidates", len(result.ToolCalls))
		return nil, nil, errors.New("no tools allowed for execution")
	}

	l.r.logger.Info(ctx, "Executing allowed tool calls", "count", len(allowed))
	if l.parentTracker != nil {
		ids := collectToolCallIDs(allowed)
		if len(ids) > 0 && l.parentTracker.registerDiscovered(ids) {
			if l.base.RunContext.ParentRunID == "" || l.base.RunContext.ParentAgentID == "" {
				return nil, nil, errors.New("nested run is missing parent run context")
			}
			if err := l.r.publishHook(
				ctx,
				hooks.NewToolCallUpdatedEvent(
					l.base.RunContext.ParentRunID,
					l.base.RunContext.ParentAgentID,
					l.base.RunContext.SessionID,
					l.parentTracker.parentToolCallID,
					l.parentTracker.currentTotal(),
				),
				l.turnID,
			); err != nil {
				return nil, nil, err
			}
			l.parentTracker.markUpdated()
		}
	}

	allowed, budgetCost := l.r.capAllowedCalls(allowed, l.st.Caps)
	if len(allowed) == 0 {
		batch.finalize = &stepFinalization{
			reason:     planner.TerminationReasonToolCap,
			skippedErr: "tool cap finalization skipped without hard deadline",
		}
		return nil, nil, nil
	}
	batch.budgetCost += budgetCost
	allowed = l.r.prepareAllowedCallsMetadata(l.input.AgentID, l.base, allowed, l.parentTracker)
	if program.kind == stepKindToolTerminal {
		if err := l.r.validateToolTerminalProgram(allowed); err != nil {
			return nil, nil, err
		}
	}

	toExecute, confirmations, err := l.r.splitConfirmationCalls(ctx, l.base, allowed)
	if err != nil {
		return nil, nil, err
	}
	if len(confirmations) > 0 && l.ctrl == nil {
		return nil, nil, errors.New("confirmation required but interrupts are not available")
	}
	if program.kind == stepKindToolTerminal && len(confirmations) > 0 {
		return nil, nil, errors.New("workflow step terminal payload cannot accompany confirmation-gated tools")
	}
	if err := l.r.recordAssistantTurn(ctx, l.input.AgentID, l.base, l.st.Transcript, allowed, l.turnID); err != nil {
		return nil, nil, err
	}

	grouped, timeouts := l.r.groupToolCallsByTimeout(toExecute, l.input, l.toolOpts.StartToCloseTimeout)
	finishBy := time.Time{}
	if !l.deadlines.Hard.IsZero() {
		finishBy = l.deadlines.Hard.Add(-l.deadlines.finalizeReserve())
	}
	outcomes, timedOut, err := l.r.executeGroupedToolCalls(l.wfCtx, l.reg, l.input.AgentID, l.base, result.ExpectedChildren, l.parentTracker, finishBy, grouped, timeouts, l.toolOpts)
	if err != nil {
		return nil, nil, err
	}
	records, err := stepToolRecordsFromExecutions(toExecute, outcomes)
	if err != nil {
		return nil, nil, err
	}
	batch.records = append(batch.records, records...)
	batch.timedOut = batch.timedOut || timedOut

	toolPauses := toolPausesFromRecords(records)
	if len(program.awaitItems) > 0 && len(toolPauses) > 0 {
		return nil, nil, errors.New("planner await and tool pause cannot both be present in the same turn")
	}
	items := append([]planner.AwaitItem(nil), program.awaitItems...)
	if len(toolPauses) > 0 {
		pauseItems, err := toolPauseAwaitItems(toolPauses)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, pauseItems...)
	}
	if program.kind == stepKindToolTerminal && len(items) > 0 {
		return nil, nil, errors.New("workflow step terminal payload cannot accompany await work")
	}
	return confirmations, items, nil
}

// recordStepToolResults appends all state derived from concrete tool results.
// Durable tool events keep every result; planner-visible transcript/tool-output
// state is filtered by the existing bookkeeping visibility contract.
func (r *Runtime) recordStepToolResults(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
	records []stepToolRecord,
) error {
	results := stepToolResults(records)
	applyToolResultPolicyHints(input, results)
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(results)...)
	if err := r.appendToolOutputRecords(ctx, st, records); err != nil {
		return err
	}
	if err := r.appendLatePlannerVisibleToolRecordUses(ctx, input.AgentID, base, records, turnID); err != nil {
		return err
	}
	if err := r.appendUserToolRecordResults(ctx, input.AgentID, base, records, turnID); err != nil {
		return err
	}
	return nil
}

// classifyStep decides whether a completed step must resume reasoning or can
// finish immediately without replaying tool results back into the planner.
func (r *Runtime) classifyStep(batch stepBatch) (stepTransition, error) {
	if batch.awaited {
		return stepTransitionResume, nil
	}
	return r.classifyToolRecords(batch.records, batch.program.result)
}

// classifyToolRecords decides whether an executed batch with no pending await
// work must resume reasoning or can complete immediately.
func (r *Runtime) classifyToolRecords(records []stepToolRecord, result *planner.PlanResult) (stepTransition, error) {
	if len(records) == 0 {
		return stepTransitionResume, nil
	}
	for _, record := range records {
		if !r.isBookkeeping(record.call.Name) {
			return stepTransitionResume, nil
		}
	}

	plannerVisibleRecords, err := r.filterPlannerVisibleToolRecords(records)
	if err != nil {
		return 0, err
	}
	if len(plannerVisibleRecords) > 0 {
		return stepTransitionResume, nil
	}

	terminal, err := r.executedSuccessfulTerminalRunTool(records)
	if err != nil {
		return 0, err
	}
	if terminal {
		return stepTransitionFinishTerminal, nil
	}
	if err := validateTerminalPlanResult(result); err != nil {
		return 0, fmt.Errorf(
			"bookkeeping-only tool batch requires a terminal tool or terminal planner payload in the same turn: %w",
			err,
		)
	}
	return stepTransitionFinishCurrent, nil
}

// executedSuccessfulTerminalRunTool reports whether the executed batch contains
// a terminal tool result that completed without a tool error.
func (r *Runtime) executedSuccessfulTerminalRunTool(records []stepToolRecord) (bool, error) {
	for _, record := range records {
		if record.result == nil {
			return false, fmt.Errorf("missing tool result for %q", record.call.Name)
		}
		spec, ok := r.toolSpec(record.result.Name)
		if !ok {
			return false, fmt.Errorf("unknown tool %q", record.result.Name)
		}
		if spec.TerminalRun && record.result.Error == nil {
			return true, nil
		}
	}
	return false, nil
}

// toolPauseAwaitItems projects runtime-owned tool pauses into the existing await
// queue item model.
func toolPauseAwaitItems(pauses []*ToolPause) ([]planner.AwaitItem, error) {
	if len(pauses) == 0 {
		return nil, nil
	}
	items := make([]planner.AwaitItem, 0, len(pauses))
	for i, pause := range pauses {
		if pause == nil || pause.Clarification == nil {
			return nil, fmt.Errorf("tool pause %d is invalid", i)
		}
		items = append(items, planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID:       pause.Clarification.ID,
			Question: pause.Clarification.Question,
		}))
	}
	return items, nil
}
