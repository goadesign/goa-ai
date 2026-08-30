// Package runtime records terminal workflow results from workflow history.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

var (
	// ErrRunCompletionNotReady indicates that the engine still reports an open
	// workflow, so no terminal result can be repaired yet.
	ErrRunCompletionNotReady = errors.New("run completion not ready")
	// ErrRunCompletionCorrupt indicates that engine history and stored run
	// identity cannot form one valid terminal record.
	ErrRunCompletionCorrupt = errors.New("run completion corrupt")
)

// RepairRunCompletion stores a missing terminal result from closed engine
// history. Ordinary reads never call this method. Repeating a successful repair
// returns without changing the stored result.
func (r *Runtime) RepairRunCompletion(ctx context.Context, runID string) error {
	meta, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	if session.IsTerminalRunStatus(meta.Status) {
		return nil
	}
	completion, err := r.Engine.QueryRunCompletion(ctx, runID)
	if err != nil {
		return fmt.Errorf("query run completion: %w", err)
	}
	if !isTerminalEngineRunStatus(completion.Status) {
		return fmt.Errorf("%w: run %q has engine status %q", ErrRunCompletionNotReady, runID, completion.Status)
	}
	turnID, err := r.repairRunTurnID(ctx, meta)
	if err != nil {
		return err
	}
	input := &RunInput{
		AgentID:   agent.Ident(meta.AgentID),
		RunID:     meta.RunID,
		SessionID: meta.SessionID,
		TurnID:    turnID,
		Labels:    cloneLabels(meta.Labels),
	}
	if err := validateRepairCompletion(completion, input); err != nil {
		return err
	}
	if completion.Output != nil && completion.Output.Suspension != nil {
		records, err := prepareRunSuspensionRecordsAt(input, completion.Output.Suspension, completion.CompletedAt)
		if err != nil {
			return fmt.Errorf("prepare repaired suspension: %w", err)
		}
		command, event, err := r.runSuspensionStorageCommand(records[0], records[1])
		if err != nil {
			return fmt.Errorf("build repaired suspension command: %w", err)
		}
		return r.repairRunSuspensionUntilApplied(ctx, command, event)
	}
	terminalStatus, phase := repairedTerminalState(completion.Status)
	completed, err := r.buildRunCompletedEvent(
		ctx,
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		terminalStatus,
		phase,
		meta.Labels,
		completion.WorkflowError,
	)
	if err != nil {
		return fmt.Errorf("build repaired terminal record: %w", err)
	}
	record, err := prepareHookRecordInputWithMetadata(completed, turnID, recordDispatchMetadata{
		TimestampMS: completion.CompletedAt.UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("prepare repaired terminal record: %w", err)
	}
	command, event, err := runTerminalStorageCommand(record)
	if err != nil {
		return fmt.Errorf("build repaired terminal command: %w", err)
	}
	return r.repairRunTerminalUntilApplied(ctx, command, event)
}

// repairRunTurnID loads the immutable start record whose envelope owns the turn
// identifier used by every later record for the run.
func (r *Runtime) repairRunTurnID(ctx context.Context, meta session.RunMeta) (string, error) {
	page, err := r.Store.ListRunRecords(ctx, meta.RunID, "", 1)
	if err != nil {
		return "", err
	}
	if len(page.Events) != 1 {
		return "", fmt.Errorf("%w: run %q has no start record", ErrRunCompletionCorrupt, meta.RunID)
	}
	started := page.Events[0]
	if started.Type != hooks.RunStarted || started.RunID != meta.RunID ||
		string(started.AgentID) != meta.AgentID || started.SessionID != meta.SessionID {
		return "", fmt.Errorf("%w: run %q start record does not match stored identity", ErrRunCompletionCorrupt, meta.RunID)
	}
	return started.TurnID, nil
}

// validateRepairCompletion rejects engine results that could not have been
// produced by the runtime's workflow contract.
func validateRepairCompletion(completion engine.RunCompletion, input *RunInput) error {
	if completion.CompletedAt.IsZero() {
		return fmt.Errorf("%w: terminal run %q has no engine completion time", ErrRunCompletionCorrupt, input.RunID)
	}
	if completion.Status == engine.RunStatusCompleted {
		if completion.WorkflowError != nil || completion.Output == nil {
			return fmt.Errorf("%w: completed run %q has no successful output", ErrRunCompletionCorrupt, input.RunID)
		}
		if err := validateWorkflowOutput(completion.Output, input.AgentID, input.RunID); err != nil {
			return fmt.Errorf("%w: run %q has invalid workflow output: %w", ErrRunCompletionCorrupt, input.RunID, err)
		}
		return nil
	}
	if completion.Output != nil || completion.WorkflowError == nil {
		return fmt.Errorf("%w: terminal run %q has inconsistent output and error", ErrRunCompletionCorrupt, input.RunID)
	}
	return nil
}

// isTerminalEngineRunStatus reports whether engine history has a final result.
func isTerminalEngineRunStatus(status engine.RunStatus) bool {
	switch status {
	case engine.RunStatusCompleted, engine.RunStatusTimedOut, engine.RunStatusFailed, engine.RunStatusCanceled:
		return true
	case engine.RunStatusPending, engine.RunStatusRunning, engine.RunStatusPaused:
		return false
	default:
		panic("runtime: unsupported engine run status: " + string(status))
	}
}

// repairedTerminalState maps the engine's final status to the stored runtime
// status and phase.
func repairedTerminalState(status engine.RunStatus) (string, run.Phase) {
	switch status {
	case engine.RunStatusCompleted:
		return runStatusSuccess, run.PhaseCompleted
	case engine.RunStatusCanceled:
		return runStatusCanceled, run.PhaseCanceled
	case engine.RunStatusTimedOut, engine.RunStatusFailed:
		return runStatusFailed, run.PhaseFailed
	case engine.RunStatusPending, engine.RunStatusRunning, engine.RunStatusPaused:
		panic("runtime: repair received non-terminal engine status: " + string(status))
	default:
		panic("runtime: repair received unsupported engine status: " + string(status))
	}
}

// buildRunCompletedEvent constructs the canonical terminal hook payload,
// including the cancellation reason stored before engine cancellation.
func (r *Runtime) buildRunCompletedEvent(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID, status string,
	phase run.Phase,
	labels map[string]string,
	err error,
) (*hooks.RunCompletedEvent, error) {
	var cancellation *run.Cancellation
	if status == runStatusCanceled {
		stored, loadErr := r.loadRunCancellation(ctx, runID)
		if loadErr != nil {
			return nil, fmt.Errorf("load cancellation provenance: %w", loadErr)
		}
		cancellation = stored
	}
	return hooks.NewRunCompletedEvent(runID, agentID, sessionID, status, phase, labels, err, cancellation), nil
}

// terminalRunStatusForError maps workflow completion errors onto the public
// runtime status contract.
func terminalRunStatusForError(err error) string {
	switch {
	case err == nil:
		return runStatusSuccess
	case isRunTimeoutError(err):
		return runStatusFailed
	case isRunCancellationError(err):
		return runStatusCanceled
	default:
		return runStatusFailed
	}
}

// terminalRunPhaseForStatus maps a terminal status to its matching phase.
func terminalRunPhaseForStatus(status string) run.Phase {
	switch status {
	case runStatusSuccess:
		return run.PhaseCompleted
	case runStatusCanceled:
		return run.PhaseCanceled
	case runStatusFailed:
		return run.PhaseFailed
	default:
		panic("runtime: unsupported terminal status: " + status)
	}
}

func isRunTimeoutError(err error) bool {
	var timeoutErr *temporal.TimeoutError
	return errors.As(err, &timeoutErr) || errors.Is(err, context.DeadlineExceeded)
}

func isRunCancellationError(err error) bool {
	var canceledErr *temporal.CanceledError
	return errors.As(err, &canceledErr) || errors.Is(err, context.Canceled)
}
