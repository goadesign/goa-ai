package runtime

// workflow_step.go defines the explicit unit of progress for the durable
// workflow loop.
//
// Contract:
// - One planner result is normalized into exactly one step program.
// - Tool execution and await draining append concrete call/result records to a
//   step batch.
// - Post-step policy is evaluated once from the accumulated batch, so resume,
//   finish, and finalization decisions do not diverge across tool and await
//   paths.

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	stepKind uint8

	stepProgram struct {
		result     *planner.PlanResult
		calls      []planner.ToolRequest
		awaitItems []planner.AwaitItem
		kind       stepKind
	}

	stepToolRecord struct {
		call       planner.ToolRequest
		result     *planner.ToolResult
		resultJSON rawjson.Message
		pause      *ToolPause
	}

	stepBatch struct {
		program       stepProgram
		records       []stepToolRecord
		recorded      int
		budgetCost    int
		timedOut      bool
		awaited       bool
		confirmations int
		awaitItems    int
		finalize      *stepFinalization
	}

	stepFinalization struct {
		reason     planner.TerminationReason
		skippedErr string
	}
)

const (
	stepKindTerminal stepKind = iota + 1
	stepKindAwait
	stepKindTools
	stepKindToolTerminal
)

// String returns the stable diagnostic label for a workflow step kind.
func (k stepKind) String() string {
	switch k {
	case stepKindTerminal:
		return "terminal"
	case stepKindAwait:
		return "await"
	case stepKindTools:
		return "tools"
	case stepKindToolTerminal:
		return "tool_terminal"
	default:
		panic(fmt.Sprintf("runtime: unknown workflow step kind %d", k))
	}
}

// normalizeStep converts a planner result into the runtime's single-step
// execution model.
func (r *Runtime) normalizeStep(result *planner.PlanResult) (stepProgram, error) {
	if result == nil {
		return stepProgram{}, errors.New("workflow step received nil PlanResult")
	}
	terminalPayloads := 0
	if result.FinalResponse != nil {
		terminalPayloads++
	}
	if result.FinalToolResult != nil {
		terminalPayloads++
	}
	if terminalPayloads > 1 {
		return stepProgram{}, errors.New("workflow step received both FinalResponse and FinalToolResult")
	}

	hasCalls := len(result.ToolCalls) > 0
	hasTerminal := terminalPayloads == 1
	hasAwait := result.Await != nil
	var awaitItems []planner.AwaitItem
	if hasAwait {
		awaitItems = result.Await.Items
		if len(awaitItems) == 0 {
			return stepProgram{}, errors.New("workflow step received empty await")
		}
	}
	if !hasCalls && !hasTerminal && !hasAwait {
		return stepProgram{}, errors.New("workflow step received empty PlanResult")
	}
	if result.SynthesizeAfterTools && (!hasCalls || hasTerminal || hasAwait) {
		return stepProgram{}, errors.New("workflow step synthesis-after-tools requires only tool calls")
	}
	if result.SynthesizeAfterTools {
		if err := r.validateSynthesisAfterTools(result.ToolCalls); err != nil {
			return stepProgram{}, err
		}
	}

	if hasTerminal && hasAwait {
		return stepProgram{}, errors.New("workflow step cannot combine terminal payload and await")
	}
	if hasTerminal && !hasCalls {
		return stepProgram{
			result: result,
			kind:   stepKindTerminal,
		}, nil
	}
	if hasTerminal {
		if err := r.validateToolTerminalProgram(result.ToolCalls); err != nil {
			return stepProgram{}, err
		}
		return stepProgram{
			result: result,
			calls:  result.ToolCalls,
			kind:   stepKindToolTerminal,
		}, nil
	}
	if hasCalls {
		return stepProgram{
			result:     result,
			calls:      result.ToolCalls,
			awaitItems: awaitItems,
			kind:       stepKindTools,
		}, nil
	}
	return stepProgram{
		result:     result,
		awaitItems: awaitItems,
		kind:       stepKindAwait,
	}, nil
}

// validateSynthesisAfterTools requires a batch whose existing execution
// classification guarantees a subsequent planner resume.
func (r *Runtime) validateSynthesisAfterTools(calls []planner.ToolRequest) error {
	hasBudgeted := false
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if ok && spec.TerminalRun {
			return fmt.Errorf("workflow step synthesis-after-tools cannot include terminal tool %q", call.Name)
		}
		if !r.isBookkeeping(call.Name) {
			hasBudgeted = true
		}
	}
	if !hasBudgeted {
		return errors.New("workflow step synthesis-after-tools requires at least one budgeted tool")
	}
	return nil
}

// runStep executes one normalized planner result and applies one post-step
// transition.
func (l *workflowLoop) runStep(program stepProgram) (*RunOutput, error) {
	if len(program.calls) > 0 {
		program.calls = l.r.prepareAllowedCallsMetadata(
			l.input.AgentID,
			l.base,
			program.calls,
			l.parentTracker,
		)
		program.result.ToolCalls = program.calls
	}
	if err := validatePlanResultToolCallIDs(program.result); err != nil {
		return nil, err
	}
	if err := l.commitSelectedModelResponse(program.result); err != nil {
		return nil, err
	}
	if program.kind == stepKindTerminal {
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	}

	batch, err := l.executeStepProgram(program)
	if err != nil {
		return nil, err
	}
	return l.advanceStep(batch)
}

// validatePlanResultToolCallIDs proves that every model-facing call has one
// stable identity before the selected response is accepted or effects begin.
func validatePlanResultToolCallIDs(result *planner.PlanResult) error {
	seen := make(map[string]struct{})
	for _, call := range planResultModelToolCalls(result) {
		if call.id == "" {
			return fmt.Errorf("workflow step tool %q is missing tool_call_id", call.name)
		}
		if _, exists := seen[call.id]; exists {
			return fmt.Errorf("workflow step contains duplicate tool_call_id %s", call.id)
		}
		seen[call.id] = struct{}{}
	}
	return nil
}

// validateToolTerminalProgram enforces the only legal terminal-with-tools
// shape: non-resuming bookkeeping side effects followed by a terminal planner
// payload in the same step.
func (r *Runtime) validateToolTerminalProgram(calls []planner.ToolRequest) error {
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("workflow step terminal payload cannot accompany unknown tool %q", call.Name)
		}
		if !spec.Bookkeeping {
			return fmt.Errorf("workflow step terminal payload cannot accompany budgeted tool %q", call.Name)
		}
		if spec.TerminalRun {
			return fmt.Errorf("workflow step terminal payload cannot accompany terminal tool %q", call.Name)
		}
	}
	return nil
}

// executeStepProgram runs all immediate effects for one planner result and any
// await work that must be drained before the planner can be resumed.
func (l *workflowLoop) executeStepProgram(program stepProgram) (stepBatch, error) {
	batch := stepBatch{program: program}
	if len(program.calls) > 0 {
		confirmations, items, err := l.executeToolStep(program, &batch)
		if err != nil {
			return stepBatch{}, err
		}
		if batch.finalize != nil {
			return batch, nil
		}
		// Tool-owned pauses occur after their tool result and must expose that
		// result before the user's answer. Planner-authored awaits remain behind
		// the step barrier so all tool uses receive one correlated result message.
		if len(confirmations) == 0 && len(program.awaitItems) == 0 && len(items) > 0 {
			if err := l.recordUnrecordedStepToolResults(&batch); err != nil {
				return stepBatch{}, err
			}
		}
		if len(confirmations) > 0 || len(items) > 0 {
			if err := l.handleAwaitQueue(
				program.result.ExpectedChildren,
				confirmations,
				items,
				&batch,
			); err != nil {
				return stepBatch{}, err
			}
		}
		return batch, nil
	}

	if len(program.awaitItems) == 0 {
		return stepBatch{}, errors.New("workflow step has neither terminal payload nor executable work")
	}
	if err := l.handleAwaitQueue(
		0,
		nil,
		program.awaitItems,
		&batch,
	); err != nil {
		return stepBatch{}, err
	}
	return batch, nil
}

// advanceStep applies all post-step policy and either completes the run or
// advances state to the next planner result.
func (l *workflowLoop) advanceStep(batch stepBatch) (*RunOutput, error) {
	if err := l.recordUnrecordedStepToolResults(&batch); err != nil {
		return nil, err
	}
	if batch.finalize != nil {
		return l.finalizeStep(batch.finalize.reason, batch.finalize.skippedErr)
	}
	if batch.timedOut {
		out, finalized, err := l.tryFinalizeStep(planner.TerminationReasonTimeBudget)
		if err != nil {
			return nil, err
		}
		if finalized {
			return out, nil
		}
	}

	if batch.program.kind == stepKindToolTerminal {
		if err := validateToolTerminalBatch(batch.records); err != nil {
			return nil, err
		}
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	}

	l.st.Caps.RemainingToolCalls = decrementCap(l.st.Caps.RemainingToolCalls, batch.budgetCost)

	resolution, err := l.r.classifyStep(batch)
	if err != nil {
		return nil, err
	}
	switch resolution {
	case stepTransitionFinishTerminal:
		return l.r.finishAfterTerminalToolCalls(l.wfCtx.Context(), l.input, l.base, l.st)
	case stepTransitionFinishCurrent:
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	case stepTransitionResume:
	}

	results := batch.results()
	// The failure streak counts planner decision points whose budgeted work
	// failed outright: any budgeted success resets the streak, an all-failure
	// batch consumes one unit regardless of its parallel width, and
	// bookkeeping results never move the counter. One exploratory batch that
	// partially fails is progress, not thrash.
	progress, failed := l.r.budgetedBatchOutcome(batch.records)
	if applyFailureStreak(&l.st.Caps, progress, failed) {
		return l.finalizeStep(planner.TerminationReasonFailureCap, "failure-cap finalization skipped without hard deadline")
	}

	if out, err := l.r.handleMissingFieldsPolicy(
		l.wfCtx,
		l.reg,
		l.input,
		l.base,
		results,
		l.st.ToolEvents,
		l.st.ToolOutputs,
		l.st.AggUsage,
		&l.st.NextAttempt,
		l.turnID,
		l.ctrl,
		&l.deadlines,
	); err != nil {
		return nil, err
	} else if out != nil {
		return out, nil
	}

	protected, err := l.r.hardProtectionIfNeeded(l.wfCtx.Context(), l.input.AgentID, l.base, results, l.turnID)
	if err != nil {
		return nil, err
	}
	if protected {
		return l.finalizeStep(planner.TerminationReasonFailureCap, "protected finalization skipped without hard deadline")
	}

	if batch.awaited {
		if err := l.r.publishHook(
			l.wfCtx.Context(),
			hooks.NewRunResumedEvent(l.base.RunContext.RunID, l.input.AgentID, l.base.RunContext.SessionID, "await_completed", "runtime", map[string]string{
				"resumed_by":    "await_queue",
				"confirmations": fmt.Sprintf("%d", batch.confirmations),
				"items":         fmt.Sprintf("%d", batch.awaitItems),
			}, 0),
			l.turnID,
		); err != nil {
			return nil, err
		}
	}

	resumeReq, err := l.r.buildNextResumeRequest(
		l.input.AgentID,
		l.base,
		l.input.Policy,
		l.st.ToolOutputs,
		batch.program.result.SynthesizeAfterTools,
		&l.st.NextAttempt,
	)
	if err != nil {
		return nil, err
	}
	resOutput, err := l.r.runPlanActivity(l.wfCtx, l.reg.ResumeActivityName, l.resumeOpts, resumeReq, l.deadlines.Budget)
	if err != nil {
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, errors.New("plan activity returned nil result on resume")
	}
	l.st.AggUsage = addTokenUsage(l.st.AggUsage, resOutput.Usage)
	l.st.Result = resOutput.Result
	l.st.Transcript = resOutput.Transcript
	l.st.ResponseCommitted = false
	return nil, nil
}

// recordUnrecordedStepToolResults persists each concrete tool result exactly
// once, preserving transcript order when a tool result creates await work.
func (l *workflowLoop) recordUnrecordedStepToolResults(batch *stepBatch) error {
	if batch == nil {
		return errors.New("workflow step missing batch")
	}
	if batch.recorded > len(batch.records) {
		panic("runtime: recorded step tool result count exceeds batch records")
	}
	records := batch.records[batch.recorded:]
	if err := l.r.recordStepToolResults(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID, records); err != nil {
		return err
	}
	batch.recorded = len(batch.records)
	return nil
}

// finalizeStep runs a required finalization transition and fails if restricted
// tool mode legally prevents it.
func (l *workflowLoop) finalizeStep(reason planner.TerminationReason, skippedErr string) (*RunOutput, error) {
	out, finalized, err := l.tryFinalizeStep(reason)
	if err != nil {
		return nil, err
	}
	if finalized {
		return out, nil
	}
	return nil, errors.New(skippedErr)
}

// tryFinalizeStep invokes planner finalization under the restricted-tool
// contract and reports whether finalization was allowed.
func (l *workflowLoop) tryFinalizeStep(reason planner.TerminationReason) (*RunOutput, bool, error) {
	return l.r.finalizeWithPlannerIfAllowed(
		l.wfCtx,
		l.reg,
		l.input,
		l.base,
		l.st.ToolEvents,
		l.st.ToolOutputs,
		l.st.AggUsage,
		l.st.NextAttempt,
		l.turnID,
		reason,
		l.deadlines.Hard,
	)
}

// validateToolTerminalBatch verifies that all bookkeeping side effects in a
// tool-terminal step completed successfully and without runtime-owned awaits.
func validateToolTerminalBatch(records []stepToolRecord) error {
	for _, record := range records {
		if record.pause != nil {
			return fmt.Errorf("workflow step terminal payload cannot accompany pause from tool %q", record.call.Name)
		}
		if record.result == nil {
			return fmt.Errorf("workflow step terminal payload missing result for tool %q", record.call.Name)
		}
		if record.result.Error != nil {
			return fmt.Errorf("workflow step terminal payload cannot accompany failed tool %q: %w", record.call.Name, record.result.Error)
		}
	}
	return nil
}

// results returns the concrete tool results produced during this step.
func (b stepBatch) results() []*planner.ToolResult {
	return stepToolResults(b.records)
}

// stepToolResults returns the result side of paired step records.
func stepToolResults(records []stepToolRecord) []*planner.ToolResult {
	if len(records) == 0 {
		return nil
	}
	results := make([]*planner.ToolResult, 0, len(records))
	for _, record := range records {
		results = append(results, record.result)
	}
	return results
}

// stepToolRecordsFromExecutions pairs execution outcomes by tool-call identity
// and returns records in canonical call order. Timeout grouping may execute
// calls in a different order than the provider-authored transcript.
func stepToolRecordsFromExecutions(calls []planner.ToolRequest, outcomes []*ToolExecutionResult) ([]stepToolRecord, error) {
	if len(calls) != len(outcomes) {
		return nil, fmt.Errorf("workflow step execution mismatch: calls=%d outcomes=%d", len(calls), len(outcomes))
	}
	byID := make(map[string]*ToolExecutionResult, len(outcomes))
	for _, outcome := range outcomes {
		if outcome == nil || outcome.ToolResult == nil {
			return nil, errors.New("workflow step execution returned an empty outcome")
		}
		id := outcome.ToolResult.ToolCallID
		if id == "" {
			return nil, fmt.Errorf("workflow step execution result for %q is missing tool_call_id", outcome.ToolResult.Name)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("workflow step execution returned duplicate tool_call_id %s", id)
		}
		byID[id] = outcome
	}
	records := make([]stepToolRecord, 0, len(calls))
	for _, call := range calls {
		outcome, ok := byID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("workflow step execution missing result for %q (%s)", call.Name, call.ToolCallID)
		}
		record := stepToolRecord{
			call:   call,
			result: outcome.ToolResult,
			pause:  outcome.Pause,
		}
		if err := validateStepToolRecord("workflow step execution", record); err != nil {
			return nil, err
		}
		records = append(records, record)
		delete(byID, call.ToolCallID)
	}
	if len(byID) > 0 {
		return nil, errors.New("workflow step execution returned results for unknown tool calls")
	}
	return records, nil
}

// toolPausesFromRecords extracts current-step pause signals in canonical call
// order.
func toolPausesFromRecords(records []stepToolRecord) []*ToolPause {
	if len(records) == 0 {
		return nil
	}
	pauses := make([]*ToolPause, 0, len(records))
	for _, record := range records {
		if record.pause == nil {
			continue
		}
		pauses = append(pauses, record.pause)
	}
	return pauses
}
