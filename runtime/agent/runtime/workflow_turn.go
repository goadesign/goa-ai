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

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
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
	l.unfinishedBatch = batch
	batch.budgetCost += program.budgetCost
	l.r.logger.Info(ctx, "Executing allowed tool calls", "count", len(allowed))
	outcomes, timedOut, executionErr := l.executeImmediateToolCalls(program.immediate, result.ExpectedChildren, nil)
	records, resultErr := stepToolRecordsAfterExecution(program.immediate, outcomes, executionErr)
	l.finishToolBatches(records)
	batch.records = append(batch.records, records...)
	batch.timedOut = batch.timedOut || timedOut
	if err := errors.Join(executionErr, resultErr); err != nil {
		return nil, nil, err
	}
	if err := l.r.validateTerminalToolClarifications(records); err != nil {
		return nil, nil, err
	}

	toolClarifications := toolClarificationsFromRecords(records)
	pending, err := pendingInputsFromToolRecords(records)
	if err != nil {
		return nil, nil, err
	}
	batch.pending = append(batch.pending, pending...)
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

// pendingInputsFromToolRecords preserves unfinished service and child work as
// requests for more input. Both ordinary execution and an approved call use
// these requests to suspend before a later workflow consumes the answer.
func pendingInputsFromToolRecords(records []stepToolRecord) ([]checkpointPendingInput, error) {
	var pending []checkpointPendingInput
	for i := range records {
		if records[i].mcpPending != nil {
			pending = append(pending, checkpointPendingInput{MCP: pendingMCPInput(records[i].call, records[i].mcpPending)})
			continue
		}
		if records[i].childSuspension == nil {
			continue
		}
		if err := validatePublicRunSuspension(records[i].childSuspension); err != nil {
			return nil, fmt.Errorf("validate suspended child %s: %w", records[i].call.ToolCallID, err)
		}
		pending = append(pending, checkpointPendingInput{Child: &checkpointChildContinuation{
			ToolCallID: records[i].call.ToolCallID,
			Suspension: records[i].childSuspension,
		}})
	}
	return pending, nil
}

// recordToolResultsBeforeError records completed outcomes before returning a
// step error. Input requests and suspended children stay available for cleanup;
// they have no completed result to publish.
func (l *workflowLoop) recordToolResultsBeforeError(batch *stepBatch, contractErr error) error {
	if batch == nil || batch.recorded == len(batch.records) {
		return contractErr
	}
	completed := *batch
	completed.records = make([]stepToolRecord, 0, len(batch.records)-batch.recorded)
	completed.recorded = 0
	for _, record := range batch.records[batch.recorded:] {
		if record.mcpPending == nil && record.childSuspension == nil {
			completed.records = append(completed.records, record)
		}
	}
	if err := l.recordUnrecordedStepToolResults(&completed); err != nil {
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
	candidates := append([]ToolCall(nil), program.calls...)
	for _, call := range candidates {
		if call.Registry != nil {
			call.Labels = mergeLabels(cloneLabels(l.base.RunContext.Labels), call.Labels)
			if _, err := l.r.resolveRegistryExecution(l.reg.Definition.route.ID, call); err != nil {
				return err
			}
			continue
		}
		_, ok, err := l.r.callSpec(call)
		if err != nil {
			return err
		}
		if !ok {
			return planner.NewOutputContractError(
				fmt.Errorf("planner called unregistered tool %q", call.Name),
			)
		}
	}
	candidates, err := l.r.applyPerRunOverrides(ctx, l.input, candidates)
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
	for i := range allowed {
		allowed[i].Labels = mergeLabels(
			cloneLabels(l.base.RunContext.Labels),
			allowed[i].Labels,
		)
		// Only finalization writes this reserved label. Remove values supplied
		// by planner, run, or policy labels before ordinary tool execution.
		delete(allowed[i].Labels, FinalizationReasonLabel)
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
	allowedSpecs, err := l.r.resolveBatchCallSpecs(allowed)
	if err != nil {
		return err
	}
	if err := validateTerminalRunBatchSpecs(allowedSpecs); err != nil {
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
		for index, call := range allowed {
			if allowedSpecs[index].TerminalRun {
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
		for index, call := range allowed {
			spec := allowedSpecs[index]
			if !spec.Bookkeeping {
				return fmt.Errorf("workflow step terminal payload cannot accompany budgeted tool %q", call.Name)
			}
			if spec.TerminalRun {
				return fmt.Errorf("workflow step terminal payload cannot accompany terminal tool %q", call.Name)
			}
		}
		if len(program.awaitItems) > 0 {
			return errors.New("workflow step terminal payload cannot accompany await work")
		}
	}
	immediate, confirmations, err := l.r.splitConfirmationCalls(ctx, l.base, allowed, allowedSpecs)
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
					genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(l.base.RunContext.ParentAgentID))},
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

// resolveBatchCallSpecs resolves each call's contract once so turn-preparation
// checks share the same validated ToolSpec values.
func (r *Runtime) resolveBatchCallSpecs(calls []ToolCall) ([]tools.ToolSpec, error) {
	specs := make([]tools.ToolSpec, len(calls))
	for index, call := range calls {
		spec, ok, err := r.callSpec(call)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", call.Name)
		}
		specs[index] = spec
	}
	return specs, nil
}

// validateTerminalRunBatchSpecs prevents one planner step from assigning both
// completion and continuation semantics to successful side effects.
func validateTerminalRunBatchSpecs(specs []tools.ToolSpec) error {
	hasTerminal := false
	hasNonTerminal := false
	for _, spec := range specs {
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
		spec, ok, err := r.callSpec(record.call)
		if err != nil {
			return err
		}
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
func (l *workflowLoop) executeImmediateToolCalls(calls []ToolCall, expectedChildren int, taskStates map[string]*taskExecution) ([]*ToolExecutionResult, bool, error) {
	budgeted := make([]ToolCall, 0, len(calls))
	bookkeeping := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		if l.r.isBookkeepingCall(call) {
			bookkeeping = append(bookkeeping, call)
		} else {
			budgeted = append(budgeted, call)
		}
	}
	budgetedOutcomes, budgetTimedOut, err := l.executeImmediateToolClass(
		budgeted,
		expectedChildren,
		l.deadlines.Budget,
		taskStates,
	)
	executionErr := err
	bookkeepingOutcomes, hardTimedOut, err := l.executeImmediateToolClass(
		bookkeeping,
		expectedChildren,
		l.deadlines.Hard,
		taskStates,
	)
	if err != nil {
		executionErr = errors.Join(executionErr, err)
	}
	return append(budgetedOutcomes, bookkeepingOutcomes...), budgetTimedOut || hardTimedOut, executionErr
}

func (l *workflowLoop) executeImmediateToolClass(
	calls []ToolCall,
	expectedChildren int,
	finishBy time.Time,
	taskStates map[string]*taskExecution,
) ([]*ToolExecutionResult, bool, error) {
	if len(calls) == 0 {
		return nil, false, nil
	}
	grouped, timeouts := l.r.groupToolCallsByTimeout(calls, l.input, l.toolOpts.StartToCloseTimeout)
	return l.r.executeGroupedToolCalls(
		l,
		l.wfCtx,
		l.reg,
		l.reg.Definition.route.ID,
		l.base,
		expectedChildren,
		l.parentTracker,
		finishBy,
		grouped,
		timeouts,
		l.toolOpts,
		taskStates,
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
	expiredBudgeted := make([]ToolCall, 0, len(confirmations))
	expiredBookkeeping := make([]ToolCall, 0, len(confirmations))
	for _, confirmation := range confirmations {
		bookkeeping := l.r.isBookkeepingCall(confirmation.call)
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
	expiredCalls := make([]ToolCall, 0, len(expiredBudgeted)+len(expiredBookkeeping))
	expiredCalls = append(expiredCalls, expiredBudgeted...)
	expiredCalls = append(expiredCalls, expiredBookkeeping...)
	if len(expiredCalls) == 0 {
		return remaining, nil, false, nil
	}
	budgetedOutcomes, _, err := l.executeImmediateToolClass(
		expiredBudgeted,
		expectedChildren,
		l.deadlines.Budget,
		nil,
	)
	executionErr := err
	bookkeepingOutcomes, _, err := l.executeImmediateToolClass(
		expiredBookkeeping,
		expectedChildren,
		l.deadlines.Hard,
		nil,
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
	base *workflowConversation,
	st *runLoopState,
	turnID string,
	records []stepToolRecord,
) error {
	executor, _ := input.Subject.Executor.AsAgent()

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
	if err := r.appendToolOutputRecords(st, records); err != nil {
		return err
	}
	if err := r.appendUserToolRecordResults(ctx, agent.Ident(executor), base, records, turnID); err != nil {
		return err
	}
	return nil
}

// publishStepToolSchedule closes a missing schedule lifecycle record.
func (r *Runtime) publishStepToolSchedule(
	ctx context.Context,
	input *RunInput,
	base *workflowConversation,
	turnID string,
	record *stepToolRecord,
) error {
	event := newToolCallScheduledEvent(
		base.RunContext.RunID,
		input.Subject,
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
	base *workflowConversation,
	turnID string,
	record *stepToolRecord,
) error {
	if record.resultRecord != nil {
		return r.publishPreparedHook(ctx, record.resultRecord, engine.ActivityOptions{})
	}
	resultJSON := record.resultJSON
	if len(resultJSON) == 0 {
		_, ok, err := r.callSpec(record.call)
		if err != nil {
			return err
		}
		if ok {
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
	event := hooks.NewToolResultReceivedEvent(
		base.RunContext.RunID,
		input.Subject,
		base.RunContext.SessionID,
		record.callRunID,
		record.call.Name,
		record.call.ToolCallID,
		parentToolCallID(record.call, &base.RunContext),
		resultJSON,
		record.result.ServerData,
		record.result.Blocks, preview,
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
func (r *Runtime) classifyToolRecords(records []stepToolRecord, result *PlanResult, awaited bool) (stepTransition, error) {
	if len(records) == 0 {
		return stepTransitionResume, nil
	}
	for _, record := range records {
		if !r.isBookkeepingCall(record.call) {
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
		spec, ok, err := r.callSpec(record.call)
		if err != nil {
			return false, err
		}
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
	call ToolCall,
	tr *planner.ToolResult,
) (bool, *RecordActivityInput, error) {
	parentID := parentToolCallID(call, &l.base.RunContext)
	if err := l.r.publishHook(
		ctx,
		newToolCallScheduledEvent(
			l.base.RunContext.RunID,
			l.input.Subject,
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
	_, ok, err := l.r.callSpec(call)
	if err != nil {
		return true, nil, err
	}
	if ok {
		encoded, err := l.r.materializeToolResult(ctx, call, tr)
		if err != nil {
			return true, nil, err
		}
		resultJSON = encoded
	}
	event := hooks.NewToolResultReceivedEvent(
		l.base.RunContext.RunID,
		l.input.Subject,
		l.base.RunContext.SessionID,
		call.RunID,
		call.Name,
		call.ToolCallID,
		parentID,
		resultJSON,
		nil,
		tr.Blocks, "",
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
