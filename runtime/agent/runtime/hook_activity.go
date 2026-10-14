// Package runtime applies typed storage commands outside
// workflow code. It stores lifecycle state and records together, then publishes
// committed hook events to local observers and active session streams.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/transcript"

	"go.opentelemetry.io/otel/attribute"
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
	storageCommandSeedBegin
	storageCommandSeedAppend
	storageCommandSeedPublish
	storageCommandSynchronousStart
	storageCommandPreparationBegin
	storageCommandUnstartedTerminal
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
	case storageCommandUnstartedTerminal:
		result, err = r.storeUnstartedTerminal(ctx, command.UnstartedTerminal)
	case storageCommandPreparationBegin:
		var decision genrunterminal.PreparationDecision
		decision, err = r.Store.BeginRunPreparationOperation(ctx, *command.PreparationBegin)
		result = &api.StorageActivityResult{PreparationBegin: &decision}
	case storageCommandSeedBegin:
		seed, beginErr := r.Store.BeginRunSeed(ctx, *command.SeedBegin)
		err = beginErr
		result = &api.StorageActivityResult{SeedBegin: &seed}
	case storageCommandSeedAppend:
		end, appendErr := r.Store.AppendRunSeed(ctx, *command.SeedAppend)
		err = appendErr
		result = &api.StorageActivityResult{SeedAppend: &api.SeedPositionResult{EndID: end}}
	case storageCommandSeedPublish:
		err = r.Store.PublishRunSeed(ctx, *command.SeedPublish)
		result = &api.StorageActivityResult{SeedPublish: &api.SeedPositionResult{EndID: command.SeedPublish.EndID}}
	case storageCommandAppend:
		result, err = r.appendRecords(ctx, command.Append.Records)
	case storageCommandRootStart:
		result, err = r.storeRunStart(ctx, kind, command.RootStart.RequestDigest, command.RootStart.SeedEndID, command.RootStart.Started, nil)
	case storageCommandChildStart:
		result, err = r.storeRunStart(ctx, kind, command.ChildStart.RequestDigest, command.ChildStart.SeedEndID, command.ChildStart.Started, command.ChildStart.ParentLinked)
	case storageCommandOneShotStart:
		result, err = r.storeRunStart(ctx, kind, command.OneShotStart.RequestDigest, command.OneShotStart.SeedEndID, command.OneShotStart.Started, nil)
	case storageCommandOneShotChildStart:
		result, err = r.storeRunStart(ctx, kind, command.OneShotChildStart.RequestDigest, command.OneShotChildStart.SeedEndID, command.OneShotChildStart.Started, command.OneShotChildStart.ParentLinked)
	case storageCommandSynchronousStart:
		return nil, malformedStorageCommand(errors.New("synchronous starts require SynchronousRuntime"))
	case storageCommandCancellation:
		result, err = r.cancelRun(ctx, command.Cancellation.Record)
	case storageCommandSuspension:
		result, err = r.suspendRun(ctx, command.Suspension.Checkpoint, command.Suspension.Suspended)
	case storageCommandTerminal:
		result, err = r.completeRun(ctx, command.Terminal)
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

// storeRunStart decodes and stores one explicit start operation, then validates
// its result before publishing hooks. Only engine operations carry a digest.
func (r *Runtime) storeRunStart(ctx context.Context, kind storageCommandKind, requestDigest []byte, seedEndID string, startedInput, linkedInput *RecordActivityInput) (*api.StorageActivityResult, error) {
	if len(requestDigest) != 32 {
		return nil, malformedStorageCommand(errors.New("runtime: accepted request digest must contain 32 bytes"))
	}
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
	case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal,
		storageCommandPreparationBegin, storageCommandSeedBegin, storageCommandSeedAppend, storageCommandSeedPublish,
		storageCommandSynchronousStart, storageCommandUnstartedTerminal:
		return nil, malformedStorageCommand(errors.New("runtime: start command has wrong operation"))
	}
	startedRecord := runLogEvent(startedInput, startedInput.Payload, started.Timestamp())
	start := session.RunStart{
		SeedEndID: seedEndID,
		Subject:   started.Subject(), RunID: started.RunID(), SessionID: started.SessionID(),
		ParentRunID: started.ParentRunID, PredecessorRunID: started.PredecessorRunID,
		StartedAt: time.UnixMilli(started.Timestamp()).UTC(), Labels: started.Labels,
	}
	seed, err := r.Store.LoadRunSeed(ctx, start.RunID, seedEndID)
	if err != nil {
		return nil, err
	}
	var linked *hooks.ChildRunLinkedEvent
	var linkedRecord *runlog.Event
	if linkedInput != nil {
		linkedEvent, decodeErr := hooks.DecodeFromRecordInput(linkedInput)
		if decodeErr != nil {
			return nil, malformedStorageCommand(decodeErr)
		}
		var linkedOK bool
		linked, linkedOK = linkedEvent.(*hooks.ChildRunLinkedEvent)
		if !linkedOK || linked.ChildRunID != start.RunID || linked.RunID() != start.ParentRunID {
			return nil, malformedStorageCommand(errors.New("runtime: child link does not match child start"))
		}
		if err := r.validateChildContinuationStart(ctx, start, linked, seed); err != nil {
			return nil, err
		}
		linkedRecord = runLogEvent(linkedInput, linkedInput.Payload, linked.Timestamp())
	}
	var outcome session.RunStartOutcome
	var cancellationReason string
	var runStatus session.RunStatus
	var records []storage.AppendResult
	var selectedEvents []hooks.Event
	switch kind {
	case storageCommandOneShotStart:
		var result storage.OneShotRunStartResult
		result, err = r.Store.StartOneShotRun(ctx, storage.OneShotRunStart{RequestDigest: [32]byte(requestDigest), Run: start, Started: startedRecord})
		runStatus = result.RunStatus
		outcome = result.Outcome
		cancellationReason = result.CancellationReason
		records = []storage.AppendResult{result.Record}
		if outcome == session.RunStartStop {
			records = append(records, result.Cancellation)
		}
		selectedEvents = []hooks.Event{started}
	case storageCommandOneShotChildStart:
		result, startErr := r.Store.StartOneShotChildRun(ctx, storage.OneShotChildRunStart{RequestDigest: [32]byte(requestDigest),
			Run: start, ParentLinked: linkedRecord, Started: startedRecord,
		})
		err = startErr
		runStatus = result.RunStatus
		outcome = result.Outcome
		cancellationReason = result.CancellationReason
		records = []storage.AppendResult{result.ParentRecord, result.Started}
		if outcome == session.RunStartStop {
			records = append(records, result.Cancellation)
		}
		selectedEvents = []hooks.Event{linked, started}
	case storageCommandRootStart, storageCommandChildStart:
		if kind == storageCommandRootStart {
			result, startErr := r.Store.StartRootRun(ctx, storage.RootRunStart{RequestDigest: [32]byte(requestDigest),
				Run: start, Started: startedRecord,
			})
			err = startErr
			outcome = result.Outcome
			cancellationReason = result.CancellationReason
			runStatus = result.RunStatus
			records = []storage.AppendResult{result.Started}
			selectedEvents = []hooks.Event{started}
			if outcome == session.RunStartStop {
				records = append(records, result.Cancellation)
			}
		} else {
			result, startErr := r.Store.StartChildRun(ctx, storage.ChildRunStart{RequestDigest: [32]byte(requestDigest),
				Run: start, ParentLinked: linkedRecord, Started: startedRecord,
			})
			err = startErr
			outcome = result.Outcome
			cancellationReason = result.CancellationReason
			runStatus = result.RunStatus
			records = []storage.AppendResult{result.ParentRecord, result.Started}
			selectedEvents = []hooks.Event{linked, started}
			if outcome == session.RunStartStop {
				records = append(records, result.Cancellation)
			}
		}
	case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal,
		storageCommandPreparationBegin, storageCommandSeedBegin, storageCommandSeedAppend, storageCommandSeedPublish,
		storageCommandSynchronousStart, storageCommandUnstartedTerminal:
		return nil, malformedStorageCommand(errors.New("runtime: start command has wrong operation"))
	}
	if err != nil {
		if errors.Is(err, session.ErrRunConflict) {
			return nil, engine.MarkActivityErrorNonRetryable(&engine.WorkflowStartConflictError{ID: start.RunID})
		}
		return nil, err
	}
	startResult := &api.StartRunResult{
		Outcome:         outcome,
		RunStatus:       runStatus,
		Records:         append([]storage.AppendResult(nil), records...),
		RenderedPrompts: clonePromptRenderEvents(seed.Declaration.RenderedPrompts),
	}
	if outcome == session.RunStartStop {
		startResult.CancellationReason = cancellationReason
	}
	result := &api.StorageActivityResult{}
	switch kind {
	case storageCommandRootStart:
		result.RootStart = startResult
	case storageCommandChildStart:
		result.ChildStart = startResult
	case storageCommandOneShotStart:
		result.OneShotStart = startResult
	case storageCommandOneShotChildStart:
		result.OneShotChildStart = startResult
	case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal,
		storageCommandPreparationBegin, storageCommandSeedBegin, storageCommandSeedAppend, storageCommandSeedPublish,
		storageCommandSynchronousStart, storageCommandUnstartedTerminal:
		return nil, malformedStorageCommand(errors.New("runtime: start command has wrong operation"))
	}
	if err := validateStorageResult(kind, result); err != nil {
		return nil, malformedStorageCommand(err)
	}
	// A closed run may replay its original start records, but those records
	// must not restart observers or live streams.
	if session.IsTerminalRunStatus(runStatus) && !records[0].Inserted {
		return result, nil
	}
	for index, event := range selectedEvents {
		if err := r.publishStoredHook(ctx, event, records[index]); err != nil {
			return nil, err
		}
	}
	return result, nil
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
	if kind == storageCommandOneShotStart || kind == storageCommandOneShotChildStart || kind == storageCommandSynchronousStart {
		if want != "" {
			return fmt.Errorf("runtime: one-shot start has session status %q, want empty", want)
		}
		return nil
	}
	if want != session.StatusActive && want != session.StatusEnded {
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
func (r *Runtime) completeRun(ctx context.Context, input *api.RunTerminalCommand) (*api.StorageActivityResult, error) {
	if input == nil || input.Record == nil {
		return nil, malformedStorageCommand(errors.New("runtime: completion command requires a record"))
	}
	// The same activity loads immutable labels and the saved cancellation reason
	// before every attempt. A read outage therefore cannot discard the workflow's
	// frozen result before its terminal write succeeds.
	meta, err := r.Store.LoadRun(ctx, input.Record.RunID)
	if err != nil {
		return nil, fmt.Errorf("load terminal run metadata: %w", err)
	}
	command, completed, err := runTerminalStorageCommand(input, meta)
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
func runTerminalStorageCommand(request *api.RunTerminalCommand, meta session.RunMeta) (storage.RunTerminal, *hooks.RunCompletedEvent, error) {
	if request == nil || request.Record == nil || request.Record.Type != hooks.RunCompleted {
		return storage.RunTerminal{}, nil, malformedStorageCommand(errors.New("runtime: completion command requires a run-completed record"))
	}
	input := request.Record
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
	completed.Labels = cloneLabels(meta.Labels)
	if status == session.RunStatusCanceled && meta.CancellationReason != "" {
		completed.Cancellation = &run.Cancellation{Reason: meta.CancellationReason}
	}
	command := storage.RunTerminal{RunID: input.RunID, Status: status}
	if len(request.OperationResult) != 0 {
		command.OperationResult, err = genrunoutput.DecodeOperationResult(request.OperationResult)
		if err != nil {
			return storage.RunTerminal{}, nil, malformedStorageCommand(err)
		}
	}
	if request.FailureFromOperation {
		if status != session.RunStatusFailed || command.OperationResult == nil {
			return storage.RunTerminal{}, nil, malformedStorageCommand(errors.New("runtime: selected failure source requires a failed run and operation result"))
		}
		failure, ok := command.OperationResult.Outcome.AsFailure()
		if !ok {
			return storage.RunTerminal{}, nil, malformedStorageCommand(errors.New("runtime: selected failure source requires an operation failure"))
		}
		original := hooks.RunFailureFromError(&selectedOperationError{cause: planner.NewToolError(failure.Message)})
		transported := *original
		transported.DebugMessage = ""
		if completed.Failure == nil || *completed.Failure != transported {
			return storage.RunTerminal{}, nil, malformedStorageCommand(errors.New("runtime: selected failure source has conflicting runtime failure metadata"))
		}
		completed.Failure = original
	}
	encoded, err := hooks.EncodeToRecordInput(completed, hooks.EncodeOptions{TurnID: input.TurnID, EventKey: input.EventKey, TimestampMS: input.TimestampMS})
	if err != nil {
		return storage.RunTerminal{}, nil, malformedStorageCommand(err)
	}
	command.Record = runLogEvent(encoded, encoded.Payload, completed.Timestamp())
	return command, completed, nil
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

func runLogEvent(input *RecordActivityInput, payload []byte, timestampMS int64) *runlog.Event {
	return &runlog.Event{
		EventKey: input.EventKey, RunID: input.RunID, Subject: input.Subject,

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
	selected, count = includeStorageKind(selected, count, storageCommandUnstartedTerminal, command.UnstartedTerminal != nil)
	selected, count = includeStorageKind(selected, count, storageCommandPreparationBegin, command.PreparationBegin != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSeedBegin, command.SeedBegin != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSeedAppend, command.SeedAppend != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSeedPublish, command.SeedPublish != nil)
	selected, count = includeStorageKind(selected, count, storageCommandAppend, command.Append != nil)
	selected, count = includeStorageKind(selected, count, storageCommandRootStart, command.RootStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandChildStart, command.ChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotStart, command.OneShotStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotChildStart, command.OneShotChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSynchronousStart, command.SynchronousStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandCancellation, command.Cancellation != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSuspension, command.Suspension != nil)
	selected, count = includeStorageKind(selected, count, storageCommandTerminal, command.Terminal != nil)
	if count != 1 {
		return 0, fmt.Errorf("runtime: storage command must select exactly one operation, got %d", count)
	}
	return selected, nil
}

// validateStorageResult checks the selected result, current run state, and
// committed record set before hook publication or workflow execution.
func validateStorageResult(kind storageCommandKind, result *api.StorageActivityResult) error {
	if result == nil {
		return errors.New("runtime: storage activity result is nil")
	}
	selected := storageCommandKind(0)
	count := 0
	selected, count = includeStorageKind(selected, count, storageCommandUnstartedTerminal, result.UnstartedTerminal != nil)
	selected, count = includeStorageKind(selected, count, storageCommandPreparationBegin, result.PreparationBegin != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSeedBegin, result.SeedBegin != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSeedAppend, result.SeedAppend != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSeedPublish, result.SeedPublish != nil)
	selected, count = includeStorageKind(selected, count, storageCommandAppend, result.Append != nil)
	selected, count = includeStorageKind(selected, count, storageCommandRootStart, result.RootStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandChildStart, result.ChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotStart, result.OneShotStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandOneShotChildStart, result.OneShotChildStart != nil)
	selected, count = includeStorageKind(selected, count, storageCommandSynchronousStart, result.SynchronousStart != nil)
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
	case storageCommandPreparationBegin:
		_, err := genrunterminal.EncodePreparationDecision(result.PreparationBegin)
		return err
	case storageCommandUnstartedTerminal:
		_, err := genrunterminal.EncodeUnstartedExecutionTerminalResult(result.UnstartedTerminal)
		return err
	case storageCommandSeedBegin:
		if result.SeedBegin.EndID == "" {
			return errors.New("runtime: seed begin returned no position")
		}
	case storageCommandSeedAppend:
		if result.SeedAppend.EndID == "" {
			return errors.New("runtime: seed append returned no position")
		}
	case storageCommandSeedPublish:
		if result.SeedPublish.EndID == "" {
			return errors.New("runtime: seed publication returned no position")
		}
	case storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart, storageCommandSynchronousStart:
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
		if kind == storageCommandSynchronousStart {
			start = result.SynchronousStart
		}
		if start.Outcome != session.RunStartProceed && start.Outcome != session.RunStartStop {
			return fmt.Errorf("runtime: storage result has unknown start outcome %q", start.Outcome)
		}
		switch start.RunStatus {
		case session.RunStatusRunning, session.RunStatusSuspended, session.RunStatusCompleted,
			session.RunStatusFailed, session.RunStatusCanceled:
		default:
			return fmt.Errorf("runtime: storage result has unknown run status %q", start.RunStatus)
		}
		if start.Outcome == session.RunStartProceed && start.CancellationReason != "" {
			return errors.New("runtime: proceeding start result cannot have a cancellation reason")
		}
		if start.Outcome == session.RunStartStop &&
			((start.RunStatus != session.RunStatusRunning && start.RunStatus != session.RunStatusCanceled && start.RunStatus != session.RunStatusFailed) || start.CancellationReason == "") {
			return errors.New("runtime: stopped start result requires running, canceled, or failed status and a nonempty cancellation reason")
		}
		if kind == storageCommandSynchronousStart && start.Outcome != session.RunStartProceed {
			return errors.New("runtime: one-shot start result must proceed")
		}
		recordCount := 1
		if kind == storageCommandChildStart || kind == storageCommandOneShotChildStart {
			recordCount++
		}
		if start.Outcome == session.RunStartStop {
			recordCount++
		}
		if len(start.Records) != recordCount {
			return fmt.Errorf("runtime: start result has %d records, want %d", len(start.Records), recordCount)
		}
		closedReplay := session.IsTerminalRunStatus(start.RunStatus)
		for index, record := range start.Records {
			if record.ID == "" {
				return fmt.Errorf("runtime: start record %d has no committed id", index)
			}
			if closedReplay && record.Inserted {
				return fmt.Errorf("runtime: closed run replay inserted start record %d", index)
			}
		}
		return validateStartRecordSessionStatus(kind, start.Outcome, start.Records)
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
	identity := []attribute.KeyValue{
		telemetry.AttrGenAIConversationID.String(conversationID(evt.SessionID(), evt.RunID())),
	}
	if executor, isAgent := evt.Subject().Executor.AsAgent(); isAgent {
		identity = telemetry.GenAIIdentityAttrsFor(telemetry.GenAIContext{
			ConversationID: conversationID(evt.SessionID(), evt.RunID()),
			AgentID:        string(executor), AgentName: string(executor),
		})
	}
	switch e := evt.(type) {
	case *hooks.ToolResultReceivedEvent:
		r.recordGenAIToolSpan(ctx, e, identity)
	case *hooks.ChildRunLinkedEvent:
		r.recordGenAIInvokeAgentSpan(ctx, e, identity)
	}
	return nil
}

// recordGenAIToolSpan emits the standard execute_tool span from the completed
// tool result event. The event timestamp marks completion; Duration reconstructs
// the start timestamp when available.
func (r *Runtime) recordGenAIToolSpan(ctx context.Context, evt *hooks.ToolResultReceivedEvent, identity []attribute.KeyValue) {
	attrs := identity
	attrs = append(attrs,
		telemetry.AttrGenAIOperationName.String(telemetry.GenAIOperationExecuteTool),
		telemetry.AttrGenAIToolName.String(string(evt.ToolName)),
		telemetry.AttrGenAIToolCallID.String(evt.ToolCallID),
	)
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
		trace.WithAttributes(attrs...),
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
func (r *Runtime) recordGenAIInvokeAgentSpan(ctx context.Context, evt *hooks.ChildRunLinkedEvent, identity []attribute.KeyValue) {
	ts := time.UnixMilli(evt.Timestamp()).UTC()
	attrs := identity
	attrs = append(attrs,
		telemetry.AttrGenAIOperationName.String(telemetry.GenAIOperationInvokeAgent),
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
	executor, isAgent := input.Subject.Executor.AsAgent()
	if !isAgent {
		return storage.AppendResult{}, malformedStorageCommand(errors.New("transcript record requires an Agent subject"))
	}
	if input.ResponseID != "" && input.EventKey != input.ResponseID {
		return storage.AppendResult{}, malformedStorageCommand(
			errors.New("runtime: assistant transcript response id must equal its stable event key"),
		)
	}
	messages, err := transcript.DecodeRunLogDelta(input.Payload)
	if err != nil {
		return storage.AppendResult{}, malformedStorageCommand(
			fmt.Errorf("runtime: decode transcript delta: %w", err),
		)
	}
	streamCommittedAssistantTurns := input.Type == transcript.RunLogMessagesAppended
	text := ""
	if streamCommittedAssistantTurns {
		text = (stream.AssistantTurnPayload{Messages: messages}).Text()
		if text != "" && input.ResponseID == "" {
			return storage.AppendResult{}, malformedStorageCommand(
				errors.New("runtime: assistant transcript delta is missing response id"),
			)
		}
	}
	result, err := r.Store.AppendRunRecord(ctx, &runlog.Event{
		EventKey: input.EventKey,
		RunID:    input.RunID,
		Subject:  input.Subject,

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
	if !streamCommittedAssistantTurns {
		return result, nil
	}
	if text == "" {
		return result, nil
	}
	evt := hooks.NewAssistantTurnCommittedEvent(
		input.RunID,
		agent.Ident(executor),
		input.SessionID,
		input.ResponseID,
		messages,
	)
	evt.SetTurnID(input.TurnID)
	evt.SetTimestampMS(input.TimestampMS)
	if err := r.streamSubscriber.HandleEvent(ctx, evt); err != nil {
		return storage.AppendResult{}, err
	}
	return result, nil
}

func (r *Runtime) enrichToolCallScheduledHint(ctx context.Context, evt *hooks.ToolCallScheduledEvent) (bool, error) {
	if evt == nil {
		return false, nil
	}
	if evt.DisplayHint != "" {
		return false, nil
	}
	if evt.Registry != nil {
		metadata, err := registryCallMetadata(ToolCall{Name: evt.ToolName, Registry: evt.Registry})
		if err != nil {
			return false, err
		}
		evt.DisplayHint = metadata.Title
		return true, nil
	}
	hint, err := r.renderToolCallDisplayHint(ctx, evt.ToolName, evt.Payload.RawMessage())
	if err != nil {
		return false, err
	}
	evt.DisplayHint = hint
	return true, nil
}

// validateChildContinuationStart proves a changed execution parent from its
// accepted seed and immediate saved checkpoint. It reads only named immutable
// facts; the store owns running-parent admission and exact closed-start retries.
func (r *Runtime) validateChildContinuationStart(ctx context.Context, start session.RunStart, linked *hooks.ChildRunLinkedEvent, seed storage.RunSeed) error {
	if start.PredecessorRunID == "" {
		return nil
	}
	previous, err := r.Store.LoadRun(ctx, start.PredecessorRunID)
	if err != nil {
		return err
	}
	if previous.ParentRunID == start.ParentRunID {
		return nil
	}
	parent, err := r.Store.LoadRun(ctx, start.ParentRunID)
	if err != nil {
		return err
	}
	if parent.SessionID != start.SessionID || !reflect.DeepEqual(parent.Subject, linked.Subject()) {
		return malformedStorageCommand(errors.New("runtime: child continuation execution parent owner mismatch"))
	}
	parentSeed, err := r.Store.LoadRunSeed(ctx, parent.RunID, parent.SeedEndID)
	if err != nil {
		return err
	}
	if parentSeed.Declaration.Kind != storage.SeedContinuation || parentSeed.Source == nil ||
		parentSeed.Declaration.RunID != parent.RunID || !reflect.DeepEqual(parentSeed.Declaration.Subject, parent.Subject) ||
		parentSeed.Declaration.SessionID != parent.SessionID ||
		parentSeed.Source.RunID != parentSeed.Declaration.SourceRunID || parentSeed.Source.EndID != parentSeed.Declaration.SourceEndID {
		return malformedStorageCommand(errors.New("runtime: child continuation requires the execution parent's exact continuation seed"))
	}
	suspension, err := r.LoadRunSuspension(ctx, parentSeed.Source.RunID)
	if err != nil {
		return err
	}
	// The parent already accepted its continuation through the owner's API.
	// Check its saved relationship here; this child worker does not own the
	// parent's planner or tools and must not require their local registration.
	checkpoint, err := decodeProviderCheckpoint(suspension)
	if err != nil {
		return malformedStorageCommand(err)
	}
	parentAgent, parentIsAgent := parent.Subject.Executor.AsAgent()
	if !parentIsAgent || checkpoint.AgentID != string(parentAgent) || checkpoint.SessionID != start.SessionID ||
		checkpoint.PreviousRunID != parentSeed.Source.RunID || checkpoint.HistoryEndID != parentSeed.Source.EndID {
		return malformedStorageCommand(errors.New("runtime: child continuation parent checkpoint does not match accepted seed"))
	}
	pending := checkpoint.Pending[0].Child
	if parent.CancellationReason != "" {
		// A durably accepted cancellation must settle every saved child. Ordinary
		// answer continuations still select only the first pending request.
		pending = nil
		for _, item := range checkpoint.Pending {
			if item.Child != nil && item.Child.ToolCallID == linked.ToolCallID {
				pending = item.Child
				break
			}
		}
	}
	if pending == nil || pending.ToolCallID != linked.ToolCallID {
		return malformedStorageCommand(errors.New("runtime: child continuation is not selected by the parent's pending work"))
	}
	record, ok := checkpointRecordByCallID(checkpoint.Batch.Records, pending.ToolCallID)
	if !ok || record.Call.Name != linked.ToolName || !reflect.DeepEqual(record.Call.Subject, parent.Subject) {
		return malformedStorageCommand(errors.New("runtime: child continuation does not match the saved parent call"))
	}
	childSuspension, err := r.LoadRunSuspension(ctx, start.PredecessorRunID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(pending.Suspension, childSuspension) {
		return malformedStorageCommand(errors.New("runtime: child continuation does not match the selected predecessor suspension"))
	}
	childCheckpoint, err := decodeWorkflowCheckpointState(childSuspension)
	if err != nil {
		return malformedStorageCommand(err)
	}
	childAgent, childIsAgent := start.Subject.Executor.AsAgent()
	if !childIsAgent || childCheckpoint.PreviousRunID != start.PredecessorRunID || childCheckpoint.AgentID != string(childAgent) ||
		seed.Declaration.Kind != storage.SeedContinuation || seed.Source == nil ||
		seed.Declaration.SourceRunID != start.PredecessorRunID || seed.Declaration.SourceEndID != childCheckpoint.HistoryEndID ||
		seed.Source.RunID != start.PredecessorRunID || seed.Source.EndID != childCheckpoint.HistoryEndID {
		return malformedStorageCommand(errors.New("runtime: child continuation seed does not match selected suspension history"))
	}
	return nil
}
