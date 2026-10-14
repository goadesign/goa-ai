package runtime

// A planner's selected result remains provisional while its required bookkeeping
// runs. Once every exact call has completed successfully, reporting failures may
// fail the workflow but cannot erase that selected result.

import "fmt"

// retainCompletedToolTerminalResult uses the existing whole-step bookkeeping
// rules before copying the planner's selected result. Partial, failed, or pending
// work leaves the result unaccepted for the later step decision.
func (l *workflowLoop) retainCompletedToolTerminalResult(batch stepBatch) error {
	if batch.program.kind != stepKindToolTerminal || batch.program.result.FinalToolResult == nil ||
		batch.finalize != nil || batch.timedOut || len(batch.pending) != 0 ||
		len(batch.records) < len(batch.program.calls) {
		return nil
	}
	if len(batch.records) != len(batch.program.calls) {
		return fmt.Errorf("terminal bookkeeping step returned %d results for %d calls", len(batch.records), len(batch.program.calls))
	}
	ordered, err := canonicalStepToolRecords(batch.program.calls, batch.records)
	if err != nil {
		return err
	}
	for i, record := range ordered {
		call := batch.program.calls[i]
		if record.call.ToolCallID != call.ToolCallID || record.call.Name != call.Name {
			return fmt.Errorf("terminal bookkeeping result does not match call %q (%s)", call.Name, call.ToolCallID)
		}
		if record.result == nil || record.result.Failure != nil || record.clarification != nil ||
			record.childSuspension != nil || record.mcpPending != nil || record.task != nil {
			return nil
		}
	}
	if err := validateToolTerminalBatch(ordered); err != nil {
		return err
	}
	accepted, err := operationResultFromToolEvent(finalToolResultEvent(l.base.RunContext.Tool, batch.program.result.FinalToolResult))
	if err != nil {
		return err
	}
	l.base.finalization.operationResult = accepted
	return nil
}
