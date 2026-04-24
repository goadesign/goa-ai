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

// runStep executes one normalized planner result and applies one post-step
// transition.
func (l *workflowLoop) runStep(program stepProgram) (*RunOutput, error) {
	if program.kind == stepKindTerminal {
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	}

	batch, err := l.executeStepProgram(program)
	if err != nil {
		return nil, err
	}
	return l.advanceStep(batch)
}

// validateToolTerminalProgram enforces the only legal terminal-with-tools
// shape: hidden, non-terminal bookkeeping side effects followed by a terminal
// planner payload in the same step.
func (r *Runtime) validateToolTerminalProgram(calls []planner.ToolRequest) error {
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("workflow step terminal payload cannot accompany unknown tool %q", call.Name)
		}
		if !spec.Bookkeeping {
			return fmt.Errorf("workflow step terminal payload cannot accompany budgeted tool %q", call.Name)
		}
		if spec.PlannerVisible {
			return fmt.Errorf("workflow step terminal payload cannot accompany planner-visible bookkeeping tool %q", call.Name)
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
	if batch.finalize != nil {
		return l.finalizeStep(batch.finalize.reason, batch.finalize.skippedErr)
	}
	if err := l.r.recordStepToolResults(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID, batch.records); err != nil {
		return nil, err
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
	if capFailures(results) > 0 {
		l.st.Caps.RemainingConsecutiveFailedToolCalls = decrementCap(
			l.st.Caps.RemainingConsecutiveFailedToolCalls,
			capFailures(results),
		)
		if l.st.Caps.MaxConsecutiveFailedToolCalls > 0 && l.st.Caps.RemainingConsecutiveFailedToolCalls <= 0 {
			return l.finalizeStep(planner.TerminationReasonFailureCap, "failure-cap finalization skipped without hard deadline")
		}
	} else if l.st.Caps.MaxConsecutiveFailedToolCalls > 0 {
		l.st.Caps.RemainingConsecutiveFailedToolCalls = l.st.Caps.MaxConsecutiveFailedToolCalls
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

	resumeReq, err := l.r.buildNextResumeRequest(l.input.AgentID, l.base, l.input.Policy, l.st.ToolOutputs, &l.st.NextAttempt)
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
	return nil, nil
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

// stepToolRecordsFromCallsAndResults adapts legacy helper boundaries to the
// paired record model while preserving canonical call order.
func stepToolRecordsFromCallsAndResults(context string, calls []planner.ToolRequest, results []*planner.ToolResult) ([]stepToolRecord, error) {
	if len(calls) == 0 && len(results) == 0 {
		return nil, nil
	}
	if len(calls) != len(results) {
		return nil, fmt.Errorf("%s: calls/results length mismatch (%d != %d)", context, len(calls), len(results))
	}

	resultsByToolCallID := make(map[string]*planner.ToolResult, len(results))
	for _, result := range results {
		if result == nil {
			return nil, fmt.Errorf("%s: nil tool result", context)
		}
		if result.ToolCallID == "" {
			return nil, fmt.Errorf("%s: missing result tool_call_id for %s", context, result.Name)
		}
		if _, exists := resultsByToolCallID[result.ToolCallID]; exists {
			return nil, fmt.Errorf("%s: duplicate result tool_call_id %s", context, result.ToolCallID)
		}
		resultsByToolCallID[result.ToolCallID] = result
	}

	records := make([]stepToolRecord, 0, len(calls))
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, fmt.Errorf("%s: missing call tool_call_id for %s", context, call.Name)
		}
		result, ok := resultsByToolCallID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("%s: missing result for tool_call_id %s", context, call.ToolCallID)
		}
		if result.Name != "" && result.Name != call.Name {
			return nil, fmt.Errorf("%s: result name %s does not match call %s", context, result.Name, call.Name)
		}
		records = append(records, stepToolRecord{
			call:   call,
			result: result,
		})
	}
	return records, nil
}

// stepToolRecordsFromExecutions pairs executed calls with their runtime-owned
// execution outcomes.
func stepToolRecordsFromExecutions(calls []planner.ToolRequest, outcomes []*ToolExecutionResult) ([]stepToolRecord, error) {
	if len(calls) != len(outcomes) {
		return nil, fmt.Errorf("workflow step execution mismatch: calls=%d outcomes=%d", len(calls), len(outcomes))
	}
	records := make([]stepToolRecord, 0, len(calls))
	remaining := outcomes
	for _, call := range calls {
		outcome := remaining[0]
		remaining = remaining[1:]
		if outcome == nil || outcome.ToolResult == nil {
			return nil, fmt.Errorf("workflow step execution missing result for %q (%s)", call.Name, call.ToolCallID)
		}
		records = append(records, stepToolRecord{
			call:   call,
			result: outcome.ToolResult,
			pause:  outcome.Pause,
		})
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
