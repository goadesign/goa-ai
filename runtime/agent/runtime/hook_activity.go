// Package runtime applies typed storage commands outside
// workflow code. It stores lifecycle state and records together, then publishes
// committed hook events to local observers and active session streams.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/transcript"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type storageCommandKind uint8

const (
	// storageActivityName is the engine-registered activity that applies every
	// durable runtime state change on behalf of workflow code.
	storageActivityName = "runtime.store"

	storageCommandAppend storageCommandKind = iota + 1
	storageCommandRootStart
	storageCommandChildStart
	storageCommandOneShotStart
	storageCommandOneShotChildStart
	storageCommandCancellation
	storageCommandSuspension
	storageCommandTerminal
)

// executeStorageCommand applies one explicitly selected storage operation
// outside deterministic workflow execution.
//
// Contract:
//   - The store owns the canonical runtime history. A write failure fails the
//     activity so the engine can retry the exact record.
//   - Canonical transcript message records are runtime-owned run-log records.
//     They bypass hook decoding and bus publication. Seed records rebuild run
//     snapshots only; appended records additionally fan out canonical
//     assistant-turn stream events for session-aware consumers.
//   - While the session is active, stream emission failures must fail the
//     activity so workflows can retry or stop rather than silently diverge
//     from the stream consumer's view.
//   - After the session is ended, stream emission becomes a no-op to avoid
//     "stream destroyed mid-run" turning into spurious run failures.
//   - One-shot runs have no session and never emit session stream events.
//   - Publishing to the hook bus is best-effort. The bus drives derived storage
//     (memory) and local observability, but it must not be allowed to corrupt or
//     block the canonical transcript.
//   - If an append attempt fails after a prefix, Temporal retries the same
//     command. Stable event keys make already-appended records idempotent.
func (r *Runtime) executeStorageCommand(ctx context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()
	kind, err := selectedStorageCommandKind(command)
	if err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	var result *api.StorageActivityResult
	switch kind {
	case storageCommandAppend:
		result, err = r.appendRecords(ctx, command.Append.Records)
	case storageCommandRootStart:
		result, err = r.storeRunStart(ctx, kind, command.RootStart.Started, nil)
	case storageCommandChildStart:
		result, err = r.storeRunStart(ctx, kind, command.ChildStart.Started, command.ChildStart.ParentLinked)
	case storageCommandOneShotStart:
		result, err = r.storeRunStart(ctx, kind, command.OneShotStart.Started, nil)
	case storageCommandOneShotChildStart:
		result, err = r.storeRunStart(ctx, kind, command.OneShotChildStart.Started, command.OneShotChildStart.ParentLinked)
	case storageCommandCancellation:
		result, err = r.cancelRun(ctx, command.Cancellation.Record)
	case storageCommandSuspension:
		result, err = r.suspendRun(ctx, command.Suspension.Checkpoint, command.Suspension.Suspended)
	case storageCommandTerminal:
		result, err = r.completeRun(ctx, command.Terminal.Record)
	}
	if err != nil {
		return nil, classifyStorageActivityError(err)
	}
	if err := validateStorageResult(kind, result); err != nil {
		return nil, engine.MarkActivityErrorNonRetryable(err)
	}
	return result, nil
}

// cancelRun stores the write-once cancellation reason before the engine stops
// execution.
func (r *Runtime) cancelRun(ctx context.Context, input *RecordActivityInput) (*api.StorageActivityResult, error) {
	if input == nil || input.Type != storage.CancellationRecordType {
		return nil, malformedStorageCommand(errors.New("runtime: cancellation command requires a cancellation record"))
	}
	var payload cancellationIntentPayload
	if err := json.Unmarshal(input.Payload, &payload); err != nil {
		return nil, malformedStorageCommand(fmt.Errorf("runtime: decode cancellation record: %w", err))
	}
	if payload.Reason == "" {
		return nil, malformedStorageCommand(errors.New("runtime: cancellation reason is required"))
	}
	result, err := r.Store.RecordRunCancellation(ctx, storage.RunCancellation{
		RunID:  input.RunID,
		Reason: payload.Reason,
		Record: runLogEvent(input, input.Payload, input.TimestampMS),
	})
	if errors.Is(err, session.ErrRunCancellationConflict) {
		return &api.StorageActivityResult{Cancellation: &api.RunCancellationResult{Outcome: api.RunCancellationConflict}}, nil
	}
	if err != nil {
		return nil, err
	}
	return &api.StorageActivityResult{Cancellation: &api.RunCancellationResult{
		Outcome: api.RunCancellationAccepted,
		Record:  result,
	}}, nil
}

// appendRecords stores ordinary records in command order.
func (r *Runtime) appendRecords(ctx context.Context, records []*RecordActivityInput) (*api.StorageActivityResult, error) {
	if len(records) == 0 {
		return nil, malformedStorageCommand(errors.New("runtime: append command is empty"))
	}
	results := make([]storage.AppendResult, 0, len(records))
	for index, record := range records {
		result, err := r.recordResult(ctx, record)
		if err != nil {
			return nil, fmt.Errorf("runtime: append record %d: %w", index, err)
		}
		results = append(results, result)
	}
	return &api.StorageActivityResult{Append: &api.AppendRecordsResult{Records: results}}, nil
}

// record appends and broadcasts one workflow-owned record. Activity entry
// points own heartbeats so a batch can call this helper without starting one
// heartbeat goroutine per item.
func (r *Runtime) recordResult(ctx context.Context, input *RecordActivityInput) (storage.AppendResult, error) {
	if input == nil {
		return storage.AppendResult{}, malformedStorageCommand(errors.New("runtime: record input is nil"))
	}
	if input.Type == runSuspensionRecordType {
		return storage.AppendResult{}, engine.MarkActivityErrorNonRetryable(
			errors.New("runtime: suspension checkpoint must be recorded with its suspended event"),
		)
	}
	if input.Type == transcript.RunLogMessagesSeeded || input.Type == transcript.RunLogMessagesAppended {
		return r.appendTranscriptRunLogMessages(ctx, input)
	}

	evt, err := hooks.DecodeFromRecordInput(input)
	if err != nil {
		return storage.AppendResult{}, engine.MarkActivityErrorNonRetryable(err)
	}
	payload := append([]byte(nil), input.Payload...)
	if e, ok := evt.(*hooks.ToolCallScheduledEvent); ok {
		enriched, err := r.enrichToolCallScheduledHint(ctx, e)
		if err != nil {
			if errors.Is(err, runlog.ErrEventConflict) {
				return storage.AppendResult{}, engine.MarkActivityErrorNonRetryable(err)
			}
			return storage.AppendResult{}, err
		}
		if enriched {
			reencoded, err := hooks.EncodeToRecordInput(e, hooks.EncodeOptions{
				TurnID:      input.TurnID,
				EventKey:    input.EventKey,
				TimestampMS: input.TimestampMS,
			})
			if err != nil {
				return storage.AppendResult{}, malformedStorageCommand(
					fmt.Errorf("runtime: encode enriched tool call record: %w", err),
				)
			}
			payload = append([]byte(nil), reencoded.Payload.RawMessage()...)
		}
	}
	if input.Type == hooks.RunStarted || input.Type == hooks.ChildRunLinked || input.Type == hooks.RunCompleted {
		return storage.AppendResult{}, engine.MarkActivityErrorNonRetryable(
			errors.New("runtime: lifecycle record requires its storage command"),
		)
	}
	record := runLogEvent(input, payload, evt.Timestamp())
	if _, ok := evt.(*hooks.RunSuspendedEvent); ok {
		return storage.AppendResult{}, engine.MarkActivityErrorNonRetryable(
			errors.New("runtime: suspended event must be recorded with its checkpoint"),
		)
	}
	result, err := r.Store.AppendRunRecord(ctx, record)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if err := r.publishStoredHook(ctx, evt, result); err != nil {
		return storage.AppendResult{}, err
	}
	return result, nil
}

// startRun stores a root, child, or one-shot start selected by the command.
func (r *Runtime) storeRunStart(ctx context.Context, kind storageCommandKind, startedInput, linkedInput *RecordActivityInput) (*api.StorageActivityResult, error) {
	if startedInput == nil || startedInput.Type != hooks.RunStarted {
		return nil, malformedStorageCommand(errors.New("runtime: start command requires a run-started record"))
	}
	startedEvent, err := hooks.DecodeFromRecordInput(startedInput)
	if err != nil {
		return nil, malformedStorageCommand(err)
	}
	started, ok := startedEvent.(*hooks.RunStartedEvent)
	if !ok {
		return nil, malformedStorageCommand(errors.New("runtime: start record is not run_started"))
	}
	switch kind {
	case storageCommandRootStart:
		if started.SessionID() == "" || linkedInput != nil {
			return nil, malformedStorageCommand(errors.New("runtime: root start requires a session and no parent link"))
		}
	case storageCommandChildStart:
		if started.SessionID() == "" || linkedInput == nil {
			return nil, malformedStorageCommand(errors.New("runtime: child start requires a session and parent link"))
		}
	case storageCommandOneShotStart:
		if started.SessionID() != "" || linkedInput != nil {
			return nil, malformedStorageCommand(errors.New("runtime: one-shot start cannot have a session or parent link"))
		}
	case storageCommandOneShotChildStart:
		if started.SessionID() != "" || started.ParentRunID == "" || linkedInput == nil {
			return nil, malformedStorageCommand(errors.New("runtime: one-shot child start requires a parent link and no session"))
		}
	case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal:
		return nil, malformedStorageCommand(errors.New("runtime: start command has wrong operation"))
	}
	startedRecord := runLogEvent(startedInput, startedInput.Payload, started.Timestamp())
	start := session.RunStart{
		AgentID: started.AgentID(), RunID: started.RunID(), SessionID: started.SessionID(),
		ParentRunID: started.ParentRunID, PredecessorRunID: started.PredecessorRunID,
		StartedAt: time.UnixMilli(started.Timestamp()).UTC(), Labels: started.Labels,
	}
	var outcome session.RunStartOutcome
	var records []storage.AppendResult
	var selectedEvents []hooks.Event
	switch kind {
	case storageCommandOneShotStart:
		result, startErr := r.Store.StartOneShotRun(ctx, storage.OneShotRunStart{Run: start, Started: startedRecord})
		err = startErr
		outcome = session.RunStartProceed
		records = []storage.AppendResult{result.Record}
		selectedEvents = []hooks.Event{started}
	case storageCommandOneShotChildStart:
		linkedEvent, decodeErr := hooks.DecodeFromRecordInput(linkedInput)
		if decodeErr != nil {
			return nil, malformedStorageCommand(decodeErr)
		}
		linked, linkedOK := linkedEvent.(*hooks.ChildRunLinkedEvent)
		if !linkedOK || linked.ChildRunID != start.RunID || linked.RunID() != start.ParentRunID {
			return nil, malformedStorageCommand(errors.New("runtime: child link does not match child start"))
		}
		result, startErr := r.Store.StartOneShotChildRun(ctx, storage.OneShotChildRunStart{
			Run: start, ParentLinked: runLogEvent(linkedInput, linkedInput.Payload, linked.Timestamp()), Started: startedRecord,
		})
		err = startErr
		outcome = session.RunStartProceed
		records = []storage.AppendResult{result.ParentRecord, result.Started}
		selectedEvents = []hooks.Event{linked, started}
	case storageCommandRootStart, storageCommandChildStart:
		canceledInput, canceledEvent, buildErr := canceledStartRecord(startedInput, started)
		if buildErr != nil {
			return nil, malformedStorageCommand(buildErr)
		}
		canceledRecord := runLogEvent(canceledInput, canceledInput.Payload, canceledEvent.Timestamp())
		if kind == storageCommandRootStart {
			result, startErr := r.Store.StartRootRun(ctx, storage.RootRunStart{
				Run: start, Started: startedRecord, Canceled: canceledRecord,
			})
			err = startErr
			outcome = result.Outcome
			records = []storage.AppendResult{result.Started}
			selectedEvents = []hooks.Event{started}
			if outcome == session.RunStartStop {
				records = append(records, result.Canceled)
				selectedEvents = append(selectedEvents, canceledEvent)
			}
		} else {
			linkedEvent, decodeErr := hooks.DecodeFromRecordInput(linkedInput)
			if decodeErr != nil {
				return nil, malformedStorageCommand(decodeErr)
			}
			linked, linkedOK := linkedEvent.(*hooks.ChildRunLinkedEvent)
			if !linkedOK || linked.ChildRunID != start.RunID || linked.RunID() != start.ParentRunID {
				return nil, malformedStorageCommand(errors.New("runtime: child link does not match child start"))
			}
			linkedRecord := runLogEvent(linkedInput, linkedInput.Payload, linked.Timestamp())
			result, startErr := r.Store.StartChildRun(ctx, storage.ChildRunStart{
				Run: start, ParentLinked: linkedRecord, Started: startedRecord, Canceled: canceledRecord,
			})
			err = startErr
			outcome = result.Outcome
			records = []storage.AppendResult{result.ParentRecord, result.Started}
			selectedEvents = []hooks.Event{linked, started}
			if outcome == session.RunStartStop {
				records = append(records, result.Canceled)
				selectedEvents = append(selectedEvents, canceledEvent)
			}
		}
	case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal:
		return nil, malformedStorageCommand(errors.New("runtime: start command has wrong operation"))
	}
	if err != nil {
		return nil, err
	}
	if outcome != session.RunStartProceed && outcome != session.RunStartStop {
		return nil, malformedStorageCommand(fmt.Errorf("runtime: store returned unknown start outcome %q", outcome))
	}
	if started.SessionID() == "" && outcome != session.RunStartProceed {
		return nil, malformedStorageCommand(fmt.Errorf("runtime: one-shot start returned outcome %q", outcome))
	}
	if len(records) != len(selectedEvents) {
		return nil, malformedStorageCommand(errors.New("runtime: store returned the wrong number of start record results"))
	}
	if err := validateStartRecordSessionStatus(kind, outcome, records); err != nil {
		return nil, malformedStorageCommand(err)
	}
	for index, event := range selectedEvents {
		if err := r.publishStoredHook(ctx, event, records[index]); err != nil {
			return nil, err
		}
	}
	startResult := &api.StartRunResult{
		Outcome: outcome,
		Records: append([]storage.AppendResult(nil), records...),
	}
	if outcome == session.RunStartStop {
		startResult.CancellationReason = run.CancellationReasonSessionEnded
	}
	switch kind {
	case storageCommandRootStart:
		return &api.StorageActivityResult{RootStart: startResult}, nil
	case storageCommandChildStart:
		return &api.StorageActivityResult{ChildStart: startResult}, nil
	case storageCommandOneShotStart:
		return &api.StorageActivityResult{OneShotStart: startResult}, nil
	case storageCommandOneShotChildStart:
		return &api.StorageActivityResult{OneShotChildStart: startResult}, nil
	case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal:
		return nil, malformedStorageCommand(errors.New("runtime: start command has wrong operation"))
	}
	return nil, malformedStorageCommand(errors.New("runtime: start command has unknown operation"))
}

// validateStartRecordSessionStatus checks that all records from one atomic
// start report the same current session state. Existing records from a
// proceeding start may report ended on an exact retry after the session closes;
// this suppresses stale streaming without changing the stored start decision.
func validateStartRecordSessionStatus(kind storageCommandKind, outcome session.RunStartOutcome, records []storage.AppendResult) error {
	want := records[0].SessionStatus
	for index, record := range records {
		if record.SessionStatus != want {
			return fmt.Errorf(
				"runtime: start record %d has session status %q, want all records to report %q",
				index,
				record.SessionStatus,
				want,
			)
		}
	}
	if kind == storageCommandOneShotStart || kind == storageCommandOneShotChildStart {
		if want != "" {
			return fmt.Errorf("runtime: one-shot start has session status %q, want empty", want)
		}
		return nil
	}
	if outcome == session.RunStartStop && want != session.StatusEnded {
		return fmt.Errorf("runtime: stopped start has session status %q, want %q", want, session.StatusEnded)
	}
	if outcome == session.RunStartProceed && want != session.StatusActive && want != session.StatusEnded {
		return fmt.Errorf("runtime: proceeding start has invalid session status %q", want)
	}
	if outcome == session.RunStartProceed && want == session.StatusEnded {
		for index, record := range records {
			if record.Inserted {
				return fmt.Errorf("runtime: newly inserted proceeding start record %d reports ended session", index)
			}
		}
	}
	return nil
}

// suspendRun commits one checkpoint and its matching suspended event.
func (r *Runtime) suspendRun(ctx context.Context, checkpoint, suspendedInput *RecordActivityInput) (*api.StorageActivityResult, error) {
	command, suspended, err := r.runSuspensionStorageCommand(checkpoint, suspendedInput)
	if err != nil {
		return nil, err
	}
	result, err := r.Store.RecordRunSuspension(ctx, command)
	if err != nil {
		return nil, err
	}
	if err := r.publishStoredHook(ctx, suspended, result); err != nil {
		return nil, err
	}
	return &api.StorageActivityResult{Suspension: &api.RecordWriteResult{Record: result}}, nil
}

// runSuspensionStorageCommand decodes the two runtime records and returns the
// single store command that commits them together.
func (r *Runtime) runSuspensionStorageCommand(checkpoint, suspendedInput *RecordActivityInput) (storage.RunSuspension, *hooks.RunSuspendedEvent, error) {
	if checkpoint == nil || suspendedInput == nil || checkpoint.Type != runSuspensionRecordType || suspendedInput.Type != hooks.RunSuspended {
		return storage.RunSuspension{}, nil, malformedStorageCommand(errors.New("runtime: suspension command requires checkpoint and suspended records"))
	}
	suspension, err := r.decodeRunSuspensionRecord(checkpoint)
	if err != nil {
		return storage.RunSuspension{}, nil, malformedStorageCommand(err)
	}
	event, err := hooks.DecodeFromRecordInput(suspendedInput)
	if err != nil {
		return storage.RunSuspension{}, nil, malformedStorageCommand(err)
	}
	suspended, ok := event.(*hooks.RunSuspendedEvent)
	if !ok || suspended.RunID() != checkpoint.RunID || suspended.SuspensionID != suspension.ID {
		return storage.RunSuspension{}, nil, malformedStorageCommand(errors.New("runtime: suspension event does not match checkpoint"))
	}
	return storage.RunSuspension{
		RunID: checkpoint.RunID, Suspension: suspension,
		Record: runLogEvent(suspendedInput, suspendedInput.Payload, suspended.Timestamp()),
	}, suspended, nil
}

// completeRun stores one completed, failed, or canceled event and the matching
// final run status in the same store command.
func (r *Runtime) completeRun(ctx context.Context, input *RecordActivityInput) (*api.StorageActivityResult, error) {
	command, completed, err := runTerminalStorageCommand(input)
	if err != nil {
		return nil, err
	}
	result, err := r.Store.RecordRunTerminal(ctx, command)
	if err != nil {
		return nil, err
	}
	if err := r.publishStoredHook(ctx, completed, result); err != nil {
		return nil, err
	}
	return &api.StorageActivityResult{Terminal: &api.RecordWriteResult{Record: result}}, nil
}

// runTerminalStorageCommand decodes a completed event and returns the store
// command that commits its final run status and record together.
func runTerminalStorageCommand(input *RecordActivityInput) (storage.RunTerminal, *hooks.RunCompletedEvent, error) {
	if input == nil || input.Type != hooks.RunCompleted {
		return storage.RunTerminal{}, nil, malformedStorageCommand(errors.New("runtime: completion command requires a run-completed record"))
	}
	event, err := hooks.DecodeFromRecordInput(input)
	if err != nil {
		return storage.RunTerminal{}, nil, malformedStorageCommand(err)
	}
	completed, ok := event.(*hooks.RunCompletedEvent)
	if !ok {
		return storage.RunTerminal{}, nil, malformedStorageCommand(errors.New("runtime: completion record is not run_completed"))
	}
	status, err := terminalStatus(completed.Status)
	if err != nil {
		return storage.RunTerminal{}, nil, malformedStorageCommand(err)
	}
	return storage.RunTerminal{
		RunID:  input.RunID,
		Status: status,
		Record: runLogEvent(input, input.Payload, completed.Timestamp()),
	}, completed, nil
}

// publishStoredHook sends a stored event to local observers and the live stream.
// Local observers see only the first insert. The live stream also sees exact
// retries because a failed delivery must be attempted again after storage has
// already accepted the record.
func (r *Runtime) publishStoredHook(ctx context.Context, event hooks.Event, result storage.AppendResult) error {
	r.publishInsertedHook(ctx, event, result)
	return r.publishStoredHookStream(ctx, event, result)
}

// publishInsertedHook sends a newly stored event to local observers. Observer
// failures are logged because the durable record remains authoritative.
func (r *Runtime) publishInsertedHook(ctx context.Context, event hooks.Event, result storage.AppendResult) {
	if result.Inserted {
		if err := r.Bus.Publish(ctx, event); err != nil {
			r.logWarn(ctx, "hook publish failed", err, "event", event.Type())
		}
	}
}

// publishStoredHookStream sends a stored event to an active session stream.
// Callers may repeat this method because keyed stream delivery is idempotent.
func (r *Runtime) publishStoredHookStream(ctx context.Context, event hooks.Event, result storage.AppendResult) error {
	if result.SessionStatus == session.StatusActive && r.streamSubscriber != nil {
		if err := r.streamSubscriber.HandleEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func canceledStartRecord(input *RecordActivityInput, started *hooks.RunStartedEvent) (*RecordActivityInput, *hooks.RunCompletedEvent, error) {
	built := hooks.NewRunCompletedEvent(
		started.RunID(), agent.Ident(started.AgentID()), started.SessionID(), runStatusCanceled,
		run.PhaseCanceled, cloneLabels(started.Labels), context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	)
	record, err := hooks.EncodeToRecordInput(built, hooks.EncodeOptions{
		TurnID: input.TurnID, EventKey: terminalRunEventKey, TimestampMS: input.TimestampMS,
	})
	if err != nil {
		return nil, nil, err
	}
	decoded, err := hooks.DecodeFromRecordInput(record)
	if err != nil {
		return nil, nil, err
	}
	event, ok := decoded.(*hooks.RunCompletedEvent)
	if !ok {
		return nil, nil, errors.New("runtime: encoded canceled start is not run_completed")
	}
	return record, event, nil
}

func runLogEvent(input *RecordActivityInput, payload []byte, timestampMS int64) *runlog.Event {
	return &runlog.Event{
		EventKey: input.EventKey, RunID: input.RunID, AgentID: input.AgentID,
		SessionID: input.SessionID, TurnID: input.TurnID, Type: input.Type,
		Payload: append([]byte(nil), payload...), Timestamp: time.UnixMilli(timestampMS).UTC(),
	}
}

func terminalStatus(status string) (session.RunStatus, error) {
	switch status {
	case runStatusSuccess:
		return session.RunStatusCompleted, nil
	case runStatusFailed:
		return session.RunStatusFailed, nil
	case runStatusCanceled:
		return session.RunStatusCanceled, nil
	default:
		return "", errors.New("runtime: run completed event has unknown status")
	}
}

// storageCommandKind validates the command union and returns its one selected
// operation.
func selectedStorageCommandKind(command *api.StorageActivityCommand) (storageCommandKind, error) {
	if command == nil {
		return 0, errors.New("runtime: storage command is nil")
	}
	selected := storageCommandKind(0)
	count := 0
	selected, count = includeStorageKind(selected, count, storageCommandAppend, command.Append != nil)
	selected, count = includeStorageKind(selected, count, storageCommandRootStart, command.RootStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandChildStart, command.ChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotStart, command.OneShotStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotChildStart, command.OneShotChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandCancellation, command.Cancellation != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSuspension, command.Suspension != nil)
	selected, count = includeStorageKind(selected, count, storageCommandTerminal, command.Terminal != nil)
	if count != 1 {
		return 0, fmt.Errorf("runtime: storage command must select exactly one operation, got %d", count)
	}
	return selected, nil
}

// validateStorageResult rejects a missing, extra, or mismatched result branch
// before workflow code can act on it.
func validateStorageResult(kind storageCommandKind, result *api.StorageActivityResult) error {
	if result == nil {
		return errors.New("runtime: storage activity result is nil")
	}
	selected := storageCommandKind(0)
	count := 0
	selected, count = includeStorageKind(selected, count, storageCommandAppend, result.Append != nil)
	selected, count = includeStorageKind(selected, count, storageCommandRootStart, result.RootStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandChildStart, result.ChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotStart, result.OneShotStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotChildStart, result.OneShotChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandCancellation, result.Cancellation != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSuspension, result.Suspension != nil)
	selected, count = includeStorageKind(selected, count, storageCommandTerminal, result.Terminal != nil)
	if count != 1 {
		return fmt.Errorf("runtime: storage result must select exactly one operation, got %d", count)
	}
	if selected != kind {
		return errors.New("runtime: storage result does not match command")
	}
	switch kind {
	case storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart:
		start := result.RootStart
		if kind == storageCommandChildStart {
			start = result.ChildStart
		}
		if kind == storageCommandOneShotStart {
			start = result.OneShotStart
		}
		if kind == storageCommandOneShotChildStart {
			start = result.OneShotChildStart
		}
		if start.Outcome != session.RunStartProceed && start.Outcome != session.RunStartStop {
			return fmt.Errorf("runtime: storage result has unknown start outcome %q", start.Outcome)
		}
		if start.Outcome == session.RunStartProceed && start.CancellationReason != "" {
			return errors.New("runtime: proceeding start result cannot have a cancellation reason")
		}
		if start.Outcome == session.RunStartStop && start.CancellationReason == "" {
			return errors.New("runtime: stopped start result requires a cancellation reason")
		}
		if (kind == storageCommandOneShotStart || kind == storageCommandOneShotChildStart) && start.Outcome != session.RunStartProceed {
			return errors.New("runtime: one-shot start result must proceed")
		}
	case storageCommandCancellation:
		if result.Cancellation.Outcome != api.RunCancellationAccepted && result.Cancellation.Outcome != api.RunCancellationConflict {
			return fmt.Errorf("runtime: storage result has unknown cancellation outcome %q", result.Cancellation.Outcome)
		}
	case storageCommandAppend, storageCommandSuspension, storageCommandTerminal:
		return nil
	}
	return nil
}

// includeStorageKind counts one present command or result branch.
func includeStorageKind(selected storageCommandKind, count int, candidate storageCommandKind, present bool) (storageCommandKind, int) {
	if present {
		return candidate, count + 1
	}
	return selected, count
}

// malformedStorageCommand marks a runtime command or result that cannot become
// valid by repeating the same activity.
func malformedStorageCommand(err error) error {
	return engine.MarkActivityErrorNonRetryable(err)
}

// classifyStorageActivityError stops retries for explicit Store contract
// rejections. Temporary database and network errors remain unchanged.
func classifyStorageActivityError(err error) error {
	if err == nil || engine.IsActivityErrorNonRetryable(err) {
		return err
	}
	var contractErr *storage.ContractError
	if errors.As(err, &contractErr) {
		return engine.MarkActivityErrorNonRetryable(err)
	}
	return err
}

// recordGenAITelemetryEvent projects durable hook records into standard GenAI
// spans as a hook subscriber. Tool spans are reconstructed from result events so
// inline, activity, and registry-backed tools share one observability shape.
func (r *Runtime) recordGenAITelemetryEvent(ctx context.Context, evt hooks.Event) error {
	switch e := evt.(type) {
	case *hooks.ToolResultReceivedEvent:
		ctx = telemetry.WithGenAIContext(ctx, telemetry.GenAIContext{
			ConversationID: conversationID(e.SessionID(), e.RunID()),
			AgentID:        e.AgentID(),
			AgentName:      e.AgentID(),
		})
		r.recordGenAIToolSpan(ctx, e)
	case *hooks.ChildRunLinkedEvent:
		ctx = telemetry.WithGenAIContext(ctx, telemetry.GenAIContext{
			ConversationID: conversationID(e.SessionID(), e.RunID()),
			AgentID:        e.AgentID(),
			AgentName:      e.AgentID(),
		})
		r.recordGenAIInvokeAgentSpan(ctx, e)
	}
	return nil
}

// recordGenAIToolSpan emits the standard execute_tool span from the completed
// tool result event. The event timestamp marks completion; Duration reconstructs
// the start timestamp when available.
func (r *Runtime) recordGenAIToolSpan(ctx context.Context, evt *hooks.ToolResultReceivedEvent) {
	endedAt := time.UnixMilli(evt.Timestamp()).UTC()
	startedAt := endedAt
	if evt.Duration > 0 {
		startedAt = endedAt.Add(-evt.Duration)
	}
	_, span := r.tracer.Start(
		ctx,
		"execute_tool "+string(evt.ToolName),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(telemetry.GenAIToolAttrs(ctx, string(evt.ToolName), evt.ToolCallID)...),
	)
	if evt.Failure != nil {
		span.RecordError(evt.Failure.Error)
		span.SetStatus(codes.Error, evt.Failure.Error.Error())
	} else {
		span.SetStatus(codes.Ok, "ok")
	}
	span.End(trace.WithTimestamp(endedAt))
}

// recordGenAIInvokeAgentSpan emits the caller-side invoke_agent span for an
// agent-as-tool child run link. The child run emits its own model/tool spans
// under its own agent identity.
func (r *Runtime) recordGenAIInvokeAgentSpan(ctx context.Context, evt *hooks.ChildRunLinkedEvent) {
	ts := time.UnixMilli(evt.Timestamp()).UTC()
	attrs := telemetry.GenAIOperationAttrs(ctx, telemetry.GenAIOperationInvokeAgent)
	attrs = append(attrs,
		telemetry.AttrGenAIToolName.String(string(evt.ToolName)),
		telemetry.AttrGenAIToolCallID.String(evt.ToolCallID),
	)
	_, span := r.tracer.Start(
		ctx,
		"invoke_agent "+string(evt.ChildAgentID),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(ts),
		trace.WithAttributes(attrs...),
	)
	span.SetStatus(codes.Ok, "ok")
	span.End(trace.WithTimestamp(ts))
}

// appendTranscriptRunLogMessages appends canonical transcript message records to
// the durable run log. Only appended transcript messages fan out canonical
// assistant-turn stream events; seeded transcript messages rebuild snapshots but
// do not represent newly committed conversation output.
func (r *Runtime) appendTranscriptRunLogMessages(ctx context.Context, input *RecordActivityInput) (storage.AppendResult, error) {
	if input == nil {
		return storage.AppendResult{}, malformedStorageCommand(errors.New("runtime: transcript delta input is nil"))
	}
	messages, err := transcript.DecodeRunLogDelta(input.Payload)
	if err != nil {
		return storage.AppendResult{}, malformedStorageCommand(
			fmt.Errorf("runtime: decode transcript delta: %w", err),
		)
	}
	result, err := r.Store.AppendRunRecord(ctx, &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   append([]byte(nil), input.Payload...),
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	})
	if err != nil {
		return storage.AppendResult{}, err
	}
	if result.SessionStatus != session.StatusActive || r.streamSubscriber == nil {
		return result, nil
	}
	streamCommittedAssistantTurns := input.Type == transcript.RunLogMessagesAppended
	if !streamCommittedAssistantTurns {
		return result, nil
	}
	for i, msg := range messages {
		if msg == nil || msg.Role != model.ConversationRoleAssistant || agentMessageText(msg) == "" {
			continue
		}
		evt := hooks.NewAssistantTurnCommittedEvent(input.RunID, input.AgentID, input.SessionID, msg)
		evt.SetTurnID(input.TurnID)
		evt.SetTimestampMS(input.TimestampMS)
		evt.SetEventKey(committedAssistantTurnEventKey(input.EventKey, i))
		if err := r.streamSubscriber.HandleEvent(ctx, evt); err != nil {
			return storage.AppendResult{}, err
		}
	}
	return result, nil
}

// committedAssistantTurnEventKey derives a stable event key for one assistant
// message extracted from a transcript delta record.
func committedAssistantTurnEventKey(base string, index int) string {
	return fmt.Sprintf("%s/assistant/%d", base, index)
}

func (r *Runtime) enrichToolCallScheduledHint(ctx context.Context, evt *hooks.ToolCallScheduledEvent) (bool, error) {
	if evt == nil {
		return false, nil
	}
	if evt.DisplayHint != "" {
		return false, nil
	}
	hint, err := r.renderToolCallDisplayHint(ctx, evt.ToolName, evt.Payload.RawMessage(), "")
	if err != nil {
		return false, err
	}
	evt.DisplayHint = hint
	return true, nil
}
