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

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
	allowed := program.allowed

	if !program.admitted {
		for _, call := range allowed {
			batch.records = append(batch.records, stepToolRecord{
				call:             call,
				scheduleRequired: true,
				scheduleExact:    true,
				result: &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: &planner.ToolFailure{
						Kind:  planner.FailureInternal,
						Error: planner.NewToolError("tool call was not executed because the run tool-call cap was exhausted"),
						Recovery: planner.RecoveryDirective{
							Action: planner.RecoveryFinish,
						},
					},
				},
			})
		}
		for i := range batch.records {
			record := &batch.records[i]
			scheduled, resultRecord, err := l.recordCapDeniedToolCall(ctx, record.call, record.result)
			if scheduled {
				record.scheduleRequired = false
			}
			record.resultRecord = resultRecord
			if err != nil {
				return nil, nil, err
			}
			record.resultPublished = true
		}
		batch.finalize = &stepFinalization{
			reason: planner.TerminationReasonToolCap,
		}
		return nil, nil, nil
	}
	batch.budgetCost += program.budgetCost
	l.r.logger.Info(ctx, "Executing allowed tool calls", "count", len(allowed))
	outcomes, timedOut, executionErr := l.executeImmediateToolCalls(program.immediate, result.ExpectedChildren)
	records, resultErr := stepToolRecordsAfterExecution(program.immediate, outcomes, executionErr)
	batch.records = append(batch.records, records...)
	batch.timedOut = batch.timedOut || timedOut
	if err := errors.Join(executionErr, resultErr); err != nil {
		return nil, nil, err
	}
	if err := l.r.validateTerminalToolClarifications(records); err != nil {
		return nil, nil, err
	}

	toolClarifications := toolClarificationsFromRecords(records)
	for i := range records {
		if records[i].childSuspension == nil {
			continue
		}
		if err := validatePublicRunSuspension(records[i].childSuspension); err != nil {
			return nil, nil, fmt.Errorf("validate suspended child %s: %w", records[i].call.ToolCallID, err)
		}
		batch.pending = append(batch.pending, checkpointPendingInput{Child: &checkpointChildContinuation{
			ToolCallID: records[i].call.ToolCallID,
			Suspension: records[i].childSuspension,
		}})
	}
	if len(program.awaitItems) > 0 && len(toolClarifications) > 0 {
		return nil, nil, errors.New("planner await and tool clarification cannot both be present in the same turn")
	}
	items := append([]planner.AwaitItem(nil), program.awaitItems...)
	if len(toolClarifications) > 0 {
		clarificationItems, err := toolClarificationAwaitItems(toolClarifications)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, clarificationItems...)
	}
	if program.kind == stepKindToolTerminal && len(items) > 0 {
		return nil, nil, errors.New("workflow step terminal payload cannot accompany await work")
	}
	return program.confirmations, items, nil
}

// recordToolResultsBeforeError preserves the committed tool-use/result
// handshake when an output-dependent contract rejects further processing.
func (l *workflowLoop) recordToolResultsBeforeError(batch *stepBatch, contractErr error) error {
	if batch == nil || batch.recorded == len(batch.records) {
		return contractErr
	}
	if err := l.recordUnrecordedStepToolResults(batch); err != nil {
		return errors.Join(contractErr, err)
	}
	return contractErr
}

// prepareToolStep resolves all runtime-owned rewrites and policy decisions
// before the assistant tool-use turn is committed. Any invalid executable shape
// therefore fails without leaving an unanswered transcript turn.
func (l *workflowLoop) prepareToolStep(program *stepProgram) error {
	ctx := l.wfCtx.Context()
	l.r.logger.Info(ctx, "Workflow received tool calls from planner", "count", len(program.calls))
	candidates, err := l.r.rewriteUnknownToolCalls(program.calls)
	if err != nil {
		return err
	}
	candidates, err = l.r.applyPerRunOverrides(ctx, l.input, candidates)
	if err != nil {
		return err
	}
	allowed, nextCaps, err := l.r.applyRuntimePolicy(
		ctx,
		l.base,
		l.input,
		candidates,
		l.st.Caps,
		l.turnID,
	)
	if err != nil {
		return err
	}
	if len(allowed) == 0 {
		l.r.logger.Error(
			ctx,
			"ERROR - No tools allowed for execution after filtering",
			"candidates",
			len(program.calls),
		)
		return errors.New("no tools allowed for execution")
	}
	if err := l.r.validateTerminalRunBatch(allowed); err != nil {
		return err
	}
	for _, call := range awaitToolRequests(program.awaitItems) {
		spec, ok := l.r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("planner await references unknown tool %q", call.Name)
		}
		if spec.TerminalRun {
			return fmt.Errorf("terminal tool %q cannot be owned by planner await work", call.Name)
		}
	}
	if len(program.awaitItems) > 0 {
		for _, call := range allowed {
			spec, ok := l.r.toolSpec(call.Name)
			if !ok {
				return fmt.Errorf("unknown tool %q", call.Name)
			}
			if spec.TerminalRun {
				return fmt.Errorf("terminal tool %q cannot accompany planner await work", call.Name)
			}
		}
	}
	budgetCost, admitted := l.r.admitToolBatch(allowed, nextCaps)
	program.allowed = allowed
	program.budgetCost = budgetCost
	program.admitted = admitted
	if !admitted {
		l.st.Caps = nextCaps
		return nil
	}
	if program.kind == stepKindToolTerminal {
		if err := l.r.validateToolTerminalProgram(allowed); err != nil {
			return err
		}
		if len(program.awaitItems) > 0 {
			return errors.New("workflow step terminal payload cannot accompany await work")
		}
	}
	immediate, confirmations, err := l.r.splitConfirmationCalls(ctx, l.base, allowed)
	if err != nil {
		return err
	}
	if program.kind == stepKindToolTerminal && len(confirmations) > 0 {
		return errors.New("workflow step terminal payload cannot accompany confirmation-gated tools")
	}
	if l.parentTracker != nil &&
		(l.base.RunContext.ParentRunID == "" || l.base.RunContext.ParentAgentID == "") {
		return errors.New("nested run is missing parent run context")
	}
	if l.parentTracker != nil {
		ids := collectToolCallIDs(allowed)
		if len(ids) > 0 && l.parentTracker.registerDiscovered(ids) {
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
				return err
			}
			l.parentTracker.markUpdated()
		}
	}
	l.st.Caps = nextCaps
	program.immediate = immediate
	program.confirmations = confirmations
	return nil
}

// validateTerminalRunBatch prevents one planner step from assigning both
// completion and continuation semantics to successful side effects.
func (r *Runtime) validateTerminalRunBatch(calls []planner.ToolRequest) error {
	hasTerminal := false
	hasNonTerminal := false
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("unknown tool %q", call.Name)
		}
		if spec.TerminalRun {
			hasTerminal = true
		} else {
			hasNonTerminal = true
		}
	}
	if hasTerminal && hasNonTerminal {
		return errors.New("workflow step cannot mix terminal and non-terminal tools")
	}
	return nil
}

// validateTerminalToolClarifications rejects a user-input request after a tool
// has declared that its successful side effect ends the run.
func (r *Runtime) validateTerminalToolClarifications(records []stepToolRecord) error {
	for _, record := range records {
		if record.clarification == nil {
			continue
		}
		spec, ok := r.toolSpec(record.call.Name)
		if !ok {
			return fmt.Errorf("unknown tool %q", record.call.Name)
		}
		if spec.TerminalRun {
			return fmt.Errorf("terminal tool %q cannot request clarification", record.call.Name)
		}
	}
	return nil
}

// executeImmediateToolCalls applies each call's deadline class independently:
// ordinary tools consume Budget, while bookkeeping obligations may use Hard.
func (l *workflowLoop) executeImmediateToolCalls(calls []planner.ToolRequest, expectedChildren int) ([]*ToolExecutionResult, bool, error) {
	budgeted := make([]planner.ToolRequest, 0, len(calls))
	bookkeeping := make([]planner.ToolRequest, 0, len(calls))
	for _, call := range calls {
		if l.r.isBookkeeping(call.Name) {
			bookkeeping = append(bookkeeping, call)
		} else {
			budgeted = append(budgeted, call)
		}
	}
	budgetedOutcomes, budgetTimedOut, err := l.executeImmediateToolClass(
		budgeted,
		expectedChildren,
		l.deadlines.Budget,
	)
	executionErr := err
	bookkeepingOutcomes, hardTimedOut, err := l.executeImmediateToolClass(
		bookkeeping,
		expectedChildren,
		l.deadlines.Hard,
	)
	if err != nil {
		executionErr = errors.Join(executionErr, err)
	}
	return append(budgetedOutcomes, bookkeepingOutcomes...), budgetTimedOut || hardTimedOut, executionErr
}

func (l *workflowLoop) executeImmediateToolClass(
	calls []planner.ToolRequest,
	expectedChildren int,
	finishBy time.Time,
) ([]*ToolExecutionResult, bool, error) {
	if len(calls) == 0 {
		return nil, false, nil
	}
	grouped, timeouts := l.r.groupToolCallsByTimeout(calls, l.input, l.toolOpts.StartToCloseTimeout)
	return l.r.executeGroupedToolCalls(
		l.wfCtx,
		l.reg,
		l.input.AgentID,
		l.base,
		expectedChildren,
		l.parentTracker,
		finishBy,
		grouped,
		timeouts,
		l.toolOpts,
	)
}

// resolveExpiredConfirmations gives every committed tool use a canonical
// timeout result once the deadline that owns that call has elapsed. Such a
// call cannot be approved successfully, so the workflow must not wait for an
// external decision.
func (l *workflowLoop) resolveExpiredConfirmations(
	confirmations []confirmationAwait,
	expectedChildren int,
) ([]confirmationAwait, []stepToolRecord, bool, error) {
	now := l.wfCtx.Now()
	remaining := make([]confirmationAwait, 0, len(confirmations))
	expiredBudgeted := make([]planner.ToolRequest, 0, len(confirmations))
	expiredBookkeeping := make([]planner.ToolRequest, 0, len(confirmations))
	for _, confirmation := range confirmations {
		bookkeeping := l.r.isBookkeeping(confirmation.call.Name)
		deadline := l.deadlines.Budget
		if bookkeeping {
			deadline = l.deadlines.Hard
		}
		if deadline.IsZero() || now.Before(deadline) {
			remaining = append(remaining, confirmation)
			continue
		}
		if bookkeeping {
			expiredBookkeeping = append(expiredBookkeeping, confirmation.call)
		} else {
			expiredBudgeted = append(expiredBudgeted, confirmation.call)
		}
	}
	expiredCalls := make([]planner.ToolRequest, 0, len(expiredBudgeted)+len(expiredBookkeeping))
	expiredCalls = append(expiredCalls, expiredBudgeted...)
	expiredCalls = append(expiredCalls, expiredBookkeeping...)
	if len(expiredCalls) == 0 {
		return remaining, nil, false, nil
	}
	budgetedOutcomes, _, err := l.executeImmediateToolClass(
		expiredBudgeted,
		expectedChildren,
		l.deadlines.Budget,
	)
	executionErr := err
	bookkeepingOutcomes, _, err := l.executeImmediateToolClass(
		expiredBookkeeping,
		expectedChildren,
		l.deadlines.Hard,
	)
	if err != nil {
		executionErr = errors.Join(executionErr, err)
	}
	records, resultErr := stepToolRecordsAfterExecution(
		expiredCalls,
		append(budgetedOutcomes, bookkeepingOutcomes...),
		executionErr,
	)
	return remaining, records, true, errors.Join(executionErr, resultErr)
}

// recordStepToolResults appends all state derived from concrete tool results.
// Provider transcript keeps every correlated result; compact ToolOutputs retain
// only results that require another planner turn.
func (r *Runtime) recordStepToolResults(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
	records []stepToolRecord,
) error {
	for i := range records {
		record := &records[i]
		if record.scheduleRequired {
			if err := r.publishStepToolSchedule(ctx, input, base, turnID, record); err != nil {
				return err
			}
			record.scheduleRequired = false
		}
		if record.callRunID == "" {
			record.callRunID = record.call.RunID
		}
		if !record.resultPublished {
			if err := r.publishStepToolResult(ctx, input, base, turnID, record); err != nil {
				return err
			}
			record.resultPublished = true
		}
		if record.resultRunID == "" {
			record.resultRunID = record.resultRecord.RunID
		}
	}
	results := stepToolResults(records)
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(results)...)
	if err := r.appendToolOutputRecords(ctx, st, records); err != nil {
		return err
	}
	if err := r.appendUserToolRecordResults(ctx, input.AgentID, base, records, turnID); err != nil {
		return err
	}
	return nil
}

// publishStepToolSchedule closes a missing schedule lifecycle record.
func (r *Runtime) publishStepToolSchedule(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	turnID string,
	record *stepToolRecord,
) error {
	event := newToolCallScheduledEvent(
		base.RunContext.RunID,
		input.AgentID,
		base.RunContext.SessionID,
		record.call,
		record.scheduleQueue,
		parentToolCallID(record.call, &base.RunContext),
		record.expectedChildren,
	)
	return r.publishHook(ctx, event, turnID)
}

// publishStepToolResult closes the canonical lifecycle for a result synthesized
// or materialized after its original result-record publication failed.
func (r *Runtime) publishStepToolResult(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	turnID string,
	record *stepToolRecord,
) error {
	if record.resultRecord != nil {
		return r.publishPreparedHook(ctx, record.resultRecord, engine.ActivityOptions{})
	}
	resultJSON := record.resultJSON
	if len(resultJSON) == 0 {
		if _, ok := r.toolSpec(record.call.Name); ok {
			encoded, err := r.materializeToolResult(ctx, record.call, record.result)
			if err != nil {
				return err
			}
			resultJSON = encoded
		}
	}
	preview, err := formatToolResultPreviewForCall(ctx, r, &record.call, record.result)
	if err != nil {
		return err
	}
	resultBytes := record.result.ResultBytes
	if !record.result.ResultOmitted {
		resultBytes = len(resultJSON)
	}
	event := hooks.NewToolResultReceivedEvent(
		base.RunContext.RunID,
		input.AgentID,
		base.RunContext.SessionID,
		record.call.Name,
		record.call.ToolCallID,
		parentToolCallID(record.call, &base.RunContext),
		resultJSON,
		resultBytes,
		record.result.ResultOmitted,
		record.result.ResultOmittedReason,
		record.result.ServerData,
		preview,
		record.result.Bounds,
		record.duration,
		record.result.Telemetry,
		record.result.Failure,
	)
	prepared, err := prepareHookRecordInput(ctx, event, turnID)
	if err != nil {
		return err
	}
	record.resultRecord = prepared
	return r.publishPreparedHook(ctx, prepared, engine.ActivityOptions{})
}

// classifyStep decides whether a completed step must resume reasoning or can
// finish immediately without replaying tool results back into the planner.
func (r *Runtime) classifyStep(batch stepBatch) (stepTransition, error) {
	if len(batch.records) == 0 {
		return stepTransitionResume, nil
	}
	return r.classifyToolRecords(batch.records, batch.program.result, batch.awaited)
}

// classifyToolRecords decides whether an executed batch must resume reasoning
// or can complete immediately.
func (r *Runtime) classifyToolRecords(records []stepToolRecord, result *planner.PlanResult, awaited bool) (stepTransition, error) {
	if len(records) == 0 {
		return stepTransitionResume, nil
	}
	for _, record := range records {
		if !r.isBookkeeping(record.call.Name) {
			return stepTransitionResume, nil
		}
	}

	resumeRecords, err := r.filterResumeRequiredToolRecords(records)
	if err != nil {
		return 0, err
	}
	if len(resumeRecords) > 0 {
		return stepTransitionResume, nil
	}

	terminal, err := r.executedSuccessfulTerminalRunTool(records)
	if err != nil {
		return 0, err
	}
	if terminal {
		return stepTransitionFinishTerminal, nil
	}
	if awaited {
		return stepTransitionResume, nil
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
		if spec.TerminalRun && record.result.Failure == nil {
			return true, nil
		}
	}
	return false, nil
}

// recordCapDeniedToolCall publishes the canonical scheduled/result handshake
// for a tool call the runtime refused to execute because the run tool-call cap
// was exhausted. The synthetic error result is planner-visible: it enters the
// provider transcript and ToolOutputs, and finalization plan activities
// rehydrate tool outputs from the canonical run log by tool_call_id. Every
// planner-visible result must therefore be durably recorded here, exactly like
// denied confirmations and canceled executions.
func (l *workflowLoop) recordCapDeniedToolCall(
	ctx context.Context,
	call planner.ToolRequest,
	tr *planner.ToolResult,
) (bool, *RecordActivityInput, error) {
	parentID := parentToolCallID(call, &l.base.RunContext)
	if err := l.r.publishHook(
		ctx,
		newToolCallScheduledEvent(
			l.base.RunContext.RunID,
			l.input.AgentID,
			l.base.RunContext.SessionID,
			call,
			"",
			parentID,
			0,
		),
		l.turnID,
	); err != nil {
		return false, nil, err
	}
	var resultJSON rawjson.Message
	if _, ok := l.r.toolSpec(call.Name); ok {
		encoded, err := l.r.materializeToolResult(ctx, call, tr)
		if err != nil {
			return true, nil, err
		}
		resultJSON = encoded
	}
	event := hooks.NewToolResultReceivedEvent(
		l.base.RunContext.RunID,
		l.input.AgentID,
		l.base.RunContext.SessionID,
		call.Name,
		call.ToolCallID,
		parentID,
		resultJSON,
		len(resultJSON),
		false,
		"",
		nil,
		"",
		nil,
		0,
		nil,
		tr.Failure,
	)
	prepared, err := prepareHookRecordInput(ctx, event, l.turnID)
	if err != nil {
		return true, nil, err
	}
	err = l.r.publishPreparedHook(ctx, prepared, engine.ActivityOptions{})
	return true, prepared, err
}

// toolClarificationAwaitItems projects tool-authored user questions into the
// planner-independent await item model.
func toolClarificationAwaitItems(clarifications []*ToolClarification) ([]planner.AwaitItem, error) {
	if len(clarifications) == 0 {
		return nil, nil
	}
	items := make([]planner.AwaitItem, 0, len(clarifications))
	for i, clarification := range clarifications {
		if clarification == nil {
			return nil, fmt.Errorf("tool clarification %d is nil", i)
		}
		items = append(items, planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID:       clarification.ID,
			Question: clarification.Question,
		}))
	}
	return items, nil
}
