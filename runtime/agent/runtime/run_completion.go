// Package runtime records terminal workflow results from workflow history.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.temporal.io/sdk/temporal"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

type storedRunEvent struct {
	record *runlog.Event
	event  hooks.Event
}

const completionRecordPageSize = 500

var (
	// ErrRunCompletionNotReady indicates that the engine still reports an open
	// workflow, so no terminal result can be ensured yet.
	ErrRunCompletionNotReady = errors.New("run completion not ready")
	// ErrRunCompletionCorrupt indicates that engine history or stored lifecycle
	// data cannot form one valid terminal record.
	ErrRunCompletionCorrupt = errors.New("run completion corrupt")
)

// EnsureRunCompletion stores a missing terminal result from closed engine
// history or redelivers the exact result already stored for a closed run.
// Ordinary reads never call this method. Repeating a successful call does not
// change the stored result or notify local observers again. Active Sessions
// require a stream configured with WithStream. Ended Sessions keep their
// durable result but suppress stream delivery.
func (r *Runtime) EnsureRunCompletion(ctx context.Context, runID string) error {
	meta, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	if session.IsTerminalRunStatus(meta.Status) {
		return r.ensureStoredRunCompletion(ctx, meta)
	}
	completion, err := r.Engine.QueryRunCompletion(ctx, runID)
	if err != nil {
		return fmt.Errorf("query run completion: %w", err)
	}
	if !isTerminalEngineRunStatus(completion.Status) {
		return fmt.Errorf("%w: run %q has engine status %q", ErrRunCompletionNotReady, runID, completion.Status)
	}
	turnID, err := r.loadRunTurnID(ctx, meta)
	if err != nil {
		return err
	}
	if meta.ParentRunID != "" {
		if _, err := r.loadStoredChildLink(ctx, meta); err != nil {
			return err
		}
	}
	input := &RunInput{
		AgentID:   agent.Ident(meta.AgentID),
		RunID:     meta.RunID,
		SessionID: meta.SessionID,
		TurnID:    turnID,
		Labels:    cloneLabels(meta.Labels),
	}
	if err := validateEngineCompletion(completion, input); err != nil {
		return err
	}
	if completion.Output != nil && completion.Output.Suspension != nil {
		records, err := prepareRunSuspensionRecordsAt(input, completion.Output.Suspension, completion.CompletedAt)
		if err != nil {
			return fmt.Errorf("prepare engine completion suspension: %w", err)
		}
		command, event, err := r.runSuspensionStorageCommand(records[0], records[1])
		if err != nil {
			return fmt.Errorf("build engine completion suspension command: %w", err)
		}
		if _, err := validateStoredRunSuspension(command.Suspension, meta); err != nil {
			return corruptStoredRunLifecycle(meta.RunID, err)
		}
		result, err := r.repairRunSuspensionUntilApplied(ctx, command)
		if err != nil {
			return err
		}
		return r.finalizeEnsuredRunCompletion(ctx, meta, event, result)
	}
	terminalStatus, phase := terminalStateFromEngineCompletion(completion.Status)
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
		return fmt.Errorf("build engine completion record: %w", err)
	}
	record, err := prepareHookRecordInputWithMetadata(completed, turnID, recordDispatchMetadata{
		TimestampMS: completion.CompletedAt.UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("prepare engine completion record: %w", err)
	}
	command, event, err := runTerminalStorageCommand(record)
	if err != nil {
		return fmt.Errorf("build engine completion command: %w", err)
	}
	result, err := r.repairRunTerminalUntilApplied(ctx, command)
	if err != nil {
		return err
	}
	return r.finalizeEnsuredRunCompletion(ctx, meta, event, result)
}

// EnsureChildRunLink redelivers the exact stored event that connects one
// session-backed child to its parent. It validates the child start and parent
// link before delivery. Active Sessions require a stream configured with
// WithStream, while ended Sessions suppress delivery. Repeating a successful
// call is safe.
func (r *Runtime) EnsureChildRunLink(ctx context.Context, runID string) error {
	meta, err := r.Store.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	if meta.SessionID == "" || meta.ParentRunID == "" {
		return corruptStoredRunLifecycle(runID, errors.New("run is not a session-backed child"))
	}
	linked, err := r.loadStoredChildPublication(ctx, meta)
	if err != nil {
		return err
	}
	status, err := r.Store.LoadSessionStatus(ctx, meta.SessionID)
	if err != nil {
		return fmt.Errorf("load child Session status: %w", err)
	}
	if err := validateEnsuredSessionStatus(meta.SessionID, status); err != nil {
		return err
	}
	if err := r.requireEnsureStream(meta.SessionID, status); err != nil {
		return err
	}
	if status == session.StatusEnded {
		return nil
	}
	return r.publishStoredHookStreamUntilApplied(ctx, linked.event, storage.AppendResult{
		ID:            linked.record.ID,
		SessionStatus: status,
	})
}

// ensureStoredRunCompletion redelivers the exact terminal event already owned
// by a closed run. It does not rebuild any value from engine history.
func (r *Runtime) ensureStoredRunCompletion(ctx context.Context, meta session.RunMeta) error {
	record, err := r.loadStoredRunCompletionRecord(ctx, meta.RunID)
	if err != nil {
		return err
	}
	switch meta.Status {
	case session.RunStatusSuspended:
		suspension, loadErr := r.Store.LoadRunSuspension(ctx, meta.RunID)
		if loadErr != nil {
			if errors.Is(loadErr, session.ErrRunSuspensionNotFound) {
				return fmt.Errorf("%w: suspended run %q has no checkpoint", ErrRunCompletionCorrupt, meta.RunID)
			}
			return fmt.Errorf("load stored run suspension: %w", loadErr)
		}
		storedSuspension, validateErr := validateStoredRunSuspension(suspension, meta)
		if validateErr != nil {
			return corruptStoredRunLifecycle(meta.RunID, validateErr)
		}
		command := storage.RunSuspension{
			RunID:      meta.RunID,
			Suspension: suspension,
			Record:     record,
		}
		if validateErr := lifecycle.ValidateRunSuspension(command, meta); validateErr != nil {
			return corruptStoredRunLifecycle(meta.RunID, validateErr)
		}
		decoded, decodeErr := hooks.DecodeRunlogEvent(record)
		if decodeErr != nil {
			return corruptStoredRunLifecycle(meta.RunID, decodeErr)
		}
		event, ok := decoded.(*hooks.RunSuspendedEvent)
		if !ok {
			return corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("record has type %q, want %q", record.Type, hooks.RunSuspended))
		}
		if validateErr := validateStoredRunSuspensionEvent(storedSuspension, event); validateErr != nil {
			return corruptStoredRunLifecycle(meta.RunID, validateErr)
		}
		result, repairErr := r.repairRunSuspensionUntilApplied(ctx, command)
		if repairErr != nil {
			return repairErr
		}
		return r.redeliverStoredRunCompletion(ctx, meta, event, result)

	case session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled:
		command := storage.RunTerminal{
			RunID:  meta.RunID,
			Status: meta.Status,
			Record: record,
		}
		if validateErr := lifecycle.ValidateRunTerminal(command, meta); validateErr != nil {
			return corruptStoredRunLifecycle(meta.RunID, validateErr)
		}
		decoded, decodeErr := hooks.DecodeRunlogEvent(record)
		if decodeErr != nil {
			return corruptStoredRunLifecycle(meta.RunID, decodeErr)
		}
		event, ok := decoded.(*hooks.RunCompletedEvent)
		if !ok {
			return corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("record has type %q, want %q", record.Type, hooks.RunCompleted))
		}
		result, repairErr := r.repairRunTerminalUntilApplied(ctx, command)
		if repairErr != nil {
			return repairErr
		}
		return r.redeliverStoredRunCompletion(ctx, meta, event, result)

	case session.RunStatusRunning:
		panic("runtime: stored completion ensure received open run status: " + string(meta.Status))

	default:
		panic("runtime: stored completion ensure received unsupported run status: " + string(meta.Status))
	}
}

// validateStoredRunSuspensionEvent checks that the public terminal record
// describes the exact checkpoint stored beside it.
func validateStoredRunSuspensionEvent(suspension *api.RunSuspension, event *hooks.RunSuspendedEvent) error {
	if event.SuspensionID != suspension.ID {
		return errors.New("suspended record has a different checkpoint id")
	}
	if event.Version != suspension.Version {
		return errors.New("suspended record has a different checkpoint version")
	}
	if event.PendingCount != len(suspension.Pending) {
		return errors.New("suspended record has a different pending input count")
	}
	if !slices.Equal(event.RequiredTools, suspension.RequiredTools) {
		return errors.New("suspended record has different required tools")
	}
	return nil
}

// finalizeEnsuredRunCompletion publishes the reconstructed event only when the
// store accepted it. If another result was stored concurrently, it reloads and
// delivers that exact event instead.
func (r *Runtime) finalizeEnsuredRunCompletion(ctx context.Context, meta session.RunMeta, event hooks.Event, result storage.RunRepairResult) error {
	if result.Outcome != storage.RunRepairDifferentTerminal {
		return r.publishEnsuredRunCompletion(ctx, meta, event, result)
	}
	stored, err := r.Store.LoadRun(ctx, meta.RunID)
	if err != nil {
		return fmt.Errorf("load stored run completion winner: %w", err)
	}
	if !session.IsTerminalRunStatus(stored.Status) {
		return corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("stored winner has open status %q", stored.Status))
	}
	if stored.Status != result.Status {
		return corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("stored winner has status %q, store reported %q", stored.Status, result.Status))
	}
	return r.ensureStoredRunCompletion(ctx, stored)
}

// redeliverStoredRunCompletion sends a validated stored event only after the
// store confirms that the supplied record exactly matches the final result.
func (r *Runtime) redeliverStoredRunCompletion(ctx context.Context, meta session.RunMeta, event hooks.Event, result storage.RunRepairResult) error {
	if result.Outcome != storage.RunRepairAlreadyStored {
		return corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("exact retry returned %q, want %q", result.Outcome, storage.RunRepairAlreadyStored))
	}
	return r.publishEnsuredRunCompletion(ctx, meta, event, result)
}

// publishEnsuredRunCompletion validates every stored event before it notifies
// local observers or sends the child link and terminal event to the stream.
// Existing event keys make every stream send an exact retry.
func (r *Runtime) publishEnsuredRunCompletion(ctx context.Context, meta session.RunMeta, terminal hooks.Event, result storage.RunRepairResult) error {
	var linked *storedRunEvent
	if meta.SessionID != "" && meta.ParentRunID != "" {
		stored, err := r.loadStoredChildPublication(ctx, meta)
		if err != nil {
			return err
		}
		linked = &stored
	}
	if err := validateEnsuredSessionStatus(meta.SessionID, result.Record.SessionStatus); err != nil {
		return err
	}
	if result.Outcome == storage.RunRepairStored {
		r.publishInsertedHook(ctx, terminal, result.Record)
	}
	if err := r.requireEnsureStream(meta.SessionID, result.Record.SessionStatus); err != nil {
		return err
	}
	if meta.SessionID == "" || result.Record.SessionStatus == session.StatusEnded {
		return nil
	}
	if linked != nil {
		if err := r.publishStoredHookStreamUntilApplied(ctx, linked.event, storage.AppendResult{
			ID:            linked.record.ID,
			SessionStatus: result.Record.SessionStatus,
		}); err != nil {
			return err
		}
	}
	return r.publishStoredHookStreamUntilApplied(ctx, terminal, result.Record)
}

// loadStoredChildPublication validates the child start and returns the exact
// parent link that stream consumers require before child completion.
func (r *Runtime) loadStoredChildPublication(ctx context.Context, meta session.RunMeta) (storedRunEvent, error) {
	if _, err := r.loadStoredRunStart(ctx, meta); err != nil {
		return storedRunEvent{}, err
	}
	return r.loadStoredChildLink(ctx, meta)
}

// loadStoredRunStart returns the one fully validated immutable start record.
func (r *Runtime) loadStoredRunStart(ctx context.Context, meta session.RunMeta) (*runlog.Event, error) {
	records, err := r.loadRunRecordsOfType(ctx, meta.RunID, hooks.RunStarted)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("run history contains %d start records, want 1", len(records)))
	}
	record := records[0]
	if err := lifecycle.ValidateStoredRunStart(record, meta); err != nil {
		return nil, corruptStoredRunLifecycle(meta.RunID, err)
	}
	return record, nil
}

// loadStoredChildLink finds and validates the parent event that names one child.
func (r *Runtime) loadStoredChildLink(ctx context.Context, meta session.RunMeta) (storedRunEvent, error) {
	parent, err := r.Store.LoadRun(ctx, meta.ParentRunID)
	if err != nil {
		return storedRunEvent{}, fmt.Errorf("load parent run for stored child link: %w", err)
	}
	records, err := r.loadRunRecordsOfType(ctx, meta.ParentRunID, hooks.ChildRunLinked)
	if err != nil {
		return storedRunEvent{}, err
	}
	var matches []storedRunEvent
	for _, record := range records {
		if err := storage.ValidateRunRecord(record); err != nil {
			return storedRunEvent{}, corruptStoredRunLifecycle(meta.RunID, err)
		}
		decoded, decodeErr := hooks.DecodeRunlogEvent(record)
		if decodeErr != nil {
			return storedRunEvent{}, corruptStoredRunLifecycle(meta.RunID, decodeErr)
		}
		linked, ok := decoded.(*hooks.ChildRunLinkedEvent)
		if !ok {
			return storedRunEvent{}, corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("record has type %q, want %q", record.Type, hooks.ChildRunLinked))
		}
		if linked.ChildRunID == meta.RunID {
			matches = append(matches, storedRunEvent{record: record, event: linked})
		}
	}
	if len(matches) != 1 {
		return storedRunEvent{}, corruptStoredRunLifecycle(meta.RunID, fmt.Errorf("parent history contains %d matching child links, want 1", len(matches)))
	}
	if err := lifecycle.ValidateStoredChildLink(matches[0].record, parent, meta); err != nil {
		return storedRunEvent{}, corruptStoredRunLifecycle(meta.RunID, err)
	}
	return matches[0], nil
}

// loadRunRecordsOfType lists every record of one type without assuming that it
// appears in the first page.
func (r *Runtime) loadRunRecordsOfType(ctx context.Context, runID string, recordType runlog.Type) ([]*runlog.Event, error) {
	var records []*runlog.Event
	for cursor := ""; ; {
		page, err := r.Store.ListRunRecords(ctx, runID, cursor, completionRecordPageSize)
		if err != nil {
			return nil, fmt.Errorf("list run %q records: %w", runID, err)
		}
		for _, record := range page.Events {
			if record == nil {
				return nil, corruptStoredRunLifecycle(runID, errors.New("run history contains a nil record"))
			}
			if record.Type == recordType {
				records = append(records, record)
			}
		}
		if page.NextCursor == "" {
			return records, nil
		}
		cursor = page.NextCursor
	}
}

// loadStoredRunCompletionRecord finds the one event that closed a run. More
// than one terminal event means the durable run history is ambiguous.
func (r *Runtime) loadStoredRunCompletionRecord(ctx context.Context, runID string) (*runlog.Event, error) {
	var completion *runlog.Event
	count := 0
	for cursor := ""; ; {
		page, err := r.Store.ListRunRecords(ctx, runID, cursor, completionRecordPageSize)
		if err != nil {
			return nil, fmt.Errorf("list stored run completion: %w", err)
		}
		for _, record := range page.Events {
			if record == nil {
				return nil, corruptStoredRunLifecycle(runID, errors.New("run history contains a nil record"))
			}
			if record.Type == hooks.RunCompleted || record.Type == hooks.RunSuspended {
				completion = record
				count++
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if count != 1 {
		return nil, corruptStoredRunLifecycle(runID, fmt.Errorf("run history contains %d completion records, want 1", count))
	}
	return completion, nil
}

// corruptStoredRunLifecycle identifies malformed or inconsistent durable run
// history while preserving the concrete reason for operators.
func corruptStoredRunLifecycle(runID string, cause error) error {
	return fmt.Errorf("%w: run %q stored lifecycle: %w", ErrRunCompletionCorrupt, runID, cause)
}

// validateEnsuredSessionStatus checks that a store result describes the
// Session named by the run. Sessionless runs must return an empty status.
func validateEnsuredSessionStatus(sessionID string, status session.SessionStatus) error {
	if sessionID == "" {
		if status != "" {
			return fmt.Errorf("runtime: sessionless ensured delivery returned Session status %q", status)
		}
		return nil
	}
	switch status {
	case session.StatusActive, session.StatusEnded:
		return nil
	default:
		return fmt.Errorf("runtime: Session %q has unsupported status %q", sessionID, status)
	}
}

// requireEnsureStream rejects delivery when an active Session has no stream.
// Ended and sessionless runs deliberately send nothing.
func (r *Runtime) requireEnsureStream(sessionID string, status session.SessionStatus) error {
	if status == session.StatusActive && r.streamSubscriber == nil {
		return fmt.Errorf("runtime: active Session %q requires Runtime.WithStream for ensured delivery", sessionID)
	}
	return nil
}

// loadRunTurnID loads the immutable start record whose envelope owns the turn
// identifier used by every later record for the run.
func (r *Runtime) loadRunTurnID(ctx context.Context, meta session.RunMeta) (string, error) {
	started, err := r.loadStoredRunStart(ctx, meta)
	if err != nil {
		return "", err
	}
	return started.TurnID, nil
}

// validateEngineCompletion rejects engine results that could not have been
// produced by the runtime's workflow contract.
func validateEngineCompletion(completion engine.RunCompletion, input *RunInput) error {
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

// terminalStateFromEngineCompletion maps the engine's final status to the
// stored runtime status and phase.
func terminalStateFromEngineCompletion(status engine.RunStatus) (string, run.Phase) {
	switch status {
	case engine.RunStatusCompleted:
		return runStatusSuccess, run.PhaseCompleted
	case engine.RunStatusCanceled:
		return runStatusCanceled, run.PhaseCanceled
	case engine.RunStatusTimedOut, engine.RunStatusFailed:
		return runStatusFailed, run.PhaseFailed
	case engine.RunStatusPending, engine.RunStatusRunning, engine.RunStatusPaused:
		panic("runtime: ensure received non-terminal engine status: " + string(status))
	default:
		panic("runtime: ensure received unsupported engine status: " + string(status))
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
