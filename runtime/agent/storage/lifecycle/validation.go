// Package lifecycle validates the typed records attached to storage lifecycle
// commands.
//
// Workflow code creates these records with the hooks codec. Host storage
// implementations call this package before changing run state so the stored
// state and durable event describe the same run, outcome, labels, and reason.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	cancellationPayload struct {
		Reason string `json:"reason"`
	}
)

// ValidateOrdinaryRunRecord rejects records that belong to a lifecycle
// operation. Those operations must store the record and matching run state
// together so history can never disagree with the run.
func ValidateOrdinaryRunRecord(record *runlog.Event) error {
	if err := storage.ValidateRunRecord(record); err != nil {
		return err
	}
	switch record.Type {
	case hooks.RunStarted,
		hooks.ChildRunLinked,
		storage.CancellationRecordType,
		hooks.RunSuspended,
		hooks.RunCompleted:
		return fmt.Errorf("record type %q requires a lifecycle operation", record.Type)
	default:
		return nil
	}
}

// ValidateRootRunStart checks that both possible root-run records describe the
// immutable run identity and the outcome each record would store.
func ValidateRootRunStart(command storage.RootRunStart) error {
	if err := session.ValidateRunStart(command.Run, false); err != nil {
		return err
	}
	if err := validateRunStartedRecord(command.Started, command.Run); err != nil {
		return fmt.Errorf("started record: %w", err)
	}
	if err := validateStoppedRecord(command.Canceled, command.Run); err != nil {
		return fmt.Errorf("canceled record: %w", err)
	}
	if command.Started.EventKey == command.Canceled.EventKey {
		return errors.New("started and canceled records require different event keys")
	}
	return nil
}

// ValidateChildRunStart checks that the parent link and both possible child
// records describe the same child identity.
func ValidateChildRunStart(command storage.ChildRunStart) error {
	if err := session.ValidateRunStart(command.Run, true); err != nil {
		return err
	}
	if err := validateChildLinkRecord(command.ParentLinked, command.Run); err != nil {
		return fmt.Errorf("parent link record: %w", err)
	}
	if err := validateRunStartedRecord(command.Started, command.Run); err != nil {
		return fmt.Errorf("started record: %w", err)
	}
	if err := validateStoppedRecord(command.Canceled, command.Run); err != nil {
		return fmt.Errorf("canceled record: %w", err)
	}
	if command.Started.EventKey == command.Canceled.EventKey {
		return errors.New("started and canceled records require different event keys")
	}
	return nil
}

// ValidateOneShotRunStart checks that a sessionless run and its first record
// carry the same immutable identity.
func ValidateOneShotRunStart(command storage.OneShotRunStart) error {
	switch {
	case command.Run.RunID == "":
		return errors.New("run id is required")
	case command.Run.AgentID == "":
		return errors.New("agent id is required")
	case command.Run.SessionID != "":
		return errors.New("one-shot run cannot have session id")
	case command.Run.ParentRunID != "":
		return errors.New("one-shot run cannot have parent run id")
	case command.Run.PredecessorRunID != "":
		return errors.New("one-shot run cannot have predecessor run id")
	case command.Run.StartedAt.IsZero():
		return errors.New("started_at is required")
	case !command.Run.StartedAt.Equal(command.Run.StartedAt.Truncate(time.Millisecond)):
		return errors.New("started_at must use millisecond precision")
	}
	if err := validateRunStartedRecord(command.Started, command.Run); err != nil {
		return fmt.Errorf("started record: %w", err)
	}
	return nil
}

// ValidateOneShotChildRunStart checks that a sessionless child and both
// relationship records carry the same immutable identity.
func ValidateOneShotChildRunStart(command storage.OneShotChildRunStart) error {
	switch {
	case command.Run.RunID == "":
		return errors.New("run id is required")
	case command.Run.AgentID == "":
		return errors.New("agent id is required")
	case command.Run.SessionID != "":
		return errors.New("one-shot child cannot have session id")
	case command.Run.ParentRunID == "":
		return session.ErrParentRunIDRequired
	case command.Run.PredecessorRunID != "":
		return errors.New("one-shot child cannot have predecessor run id")
	case command.Run.StartedAt.IsZero():
		return errors.New("started_at is required")
	case !command.Run.StartedAt.Equal(command.Run.StartedAt.Truncate(time.Millisecond)):
		return errors.New("started_at must use millisecond precision")
	}
	if err := validateChildLinkRecord(command.ParentLinked, command.Run); err != nil {
		return fmt.Errorf("parent link record: %w", err)
	}
	if err := validateRunStartedRecord(command.Started, command.Run); err != nil {
		return fmt.Errorf("started record: %w", err)
	}
	if command.ParentLinked.EventKey == command.Started.EventKey {
		return errors.New("parent link and started records require different event keys")
	}
	return nil
}

// ValidateRunCancellation checks that the record stores the reason supplied by
// the cancellation command and belongs to the stored run.
func ValidateRunCancellation(command storage.RunCancellation, meta session.RunMeta) error {
	if command.RunID == "" || command.Reason == "" {
		return errors.New("run cancellation requires run id and reason")
	}
	if command.RunID != meta.RunID {
		return errors.New("cancellation command does not match stored run")
	}
	if err := validateRecordOwner(command.Record, meta.RunID, meta.AgentID, meta.SessionID); err != nil {
		return fmt.Errorf("cancellation record: %w", err)
	}
	if command.Record.Type != storage.CancellationRecordType {
		return fmt.Errorf("cancellation record has type %q, want %q", command.Record.Type, storage.CancellationRecordType)
	}
	var payload cancellationPayload
	if err := json.Unmarshal(command.Record.Payload, &payload); err != nil {
		return fmt.Errorf("decode cancellation record: %w", err)
	}
	if payload.Reason != command.Reason {
		return errors.New("cancellation record reason does not match command")
	}
	return nil
}

// ValidateRunSuspension checks that the terminal suspension record names the
// checkpoint stored by the same command and belongs to the stored run.
func ValidateRunSuspension(command storage.RunSuspension, meta session.RunMeta) error {
	if command.RunID == "" || command.Suspension.ID == "" || len(command.Suspension.Data) == 0 {
		return errors.New("run suspension requires run id, suspension id, and data")
	}
	if command.RunID != meta.RunID {
		return errors.New("suspension command does not match stored run")
	}
	event, err := decodeHookRecord(command.Record, hooks.RunSuspended)
	if err != nil {
		return fmt.Errorf("suspension record: %w", err)
	}
	if err := validateEventOwner(event, meta.RunID, meta.AgentID, meta.SessionID); err != nil {
		return fmt.Errorf("suspension record: %w", err)
	}
	suspended := event.(*hooks.RunSuspendedEvent)
	if suspended.SuspensionID != command.Suspension.ID {
		return errors.New("suspension record does not match checkpoint")
	}
	return nil
}

// ValidateRunTerminal checks that a final record carries the requested state,
// the run's immutable labels, and the stored cancellation reason when present.
func ValidateRunTerminal(command storage.RunTerminal, meta session.RunMeta) error {
	if command.RunID == "" || !session.IsTerminalRunStatus(command.Status) || command.Status == session.RunStatusSuspended {
		return errors.New("run terminal requires completed, failed, or canceled status")
	}
	if command.RunID != meta.RunID {
		return errors.New("terminal command does not match stored run")
	}
	event, err := decodeHookRecord(command.Record, hooks.RunCompleted)
	if err != nil {
		return fmt.Errorf("terminal record: %w", err)
	}
	if err := validateEventOwner(event, meta.RunID, meta.AgentID, meta.SessionID); err != nil {
		return fmt.Errorf("terminal record: %w", err)
	}
	completed := event.(*hooks.RunCompletedEvent)
	expectedStatus, expectedPhase := terminalState(completed.Status)
	if expectedStatus == "" || expectedStatus != command.Status || completed.Phase != expectedPhase {
		return errors.New("terminal record outcome does not match command")
	}
	if !maps.Equal(completed.Labels, meta.Labels) {
		return errors.New("terminal record labels do not match run")
	}
	if command.Status == session.RunStatusCanceled {
		expectedReason := meta.CancellationReason
		if expectedReason == "" {
			expectedReason = run.CancellationReasonEngineCanceled
		}
		if completed.Cancellation.Reason != expectedReason {
			return errors.New("terminal record cancellation reason does not match run")
		}
	}
	return nil
}

// ValidateStoredRunStart checks a saved run-started event against the run data
// returned by the store. The predecessor remains in the event because run
// metadata does not keep a second copy of continuation history.
func ValidateStoredRunStart(record *runlog.Event, meta session.RunMeta) error {
	event, err := decodeHookRecord(record, hooks.RunStarted)
	if err != nil {
		return err
	}
	started := event.(*hooks.RunStartedEvent)
	start := session.RunStart{
		AgentID:          meta.AgentID,
		RunID:            meta.RunID,
		SessionID:        meta.SessionID,
		ParentRunID:      meta.ParentRunID,
		PredecessorRunID: started.PredecessorRunID,
		StartedAt:        meta.StartedAt,
		Labels:           meta.Labels,
	}
	if err := session.ValidateRunStart(start, meta.ParentRunID != ""); err != nil {
		return err
	}
	return validateRunStartedEvent(record, event, start)
}

// ValidateStoredChildLink checks a saved parent event against the parent and
// child run data returned by the store.
func ValidateStoredChildLink(record *runlog.Event, parent, child session.RunMeta) error {
	if parent.RunID != child.ParentRunID {
		return errors.New("stored parent run does not match child parent id")
	}
	if parent.SessionID != child.SessionID {
		return errors.New("stored parent and child belong to different sessions")
	}
	event, err := decodeHookRecord(record, hooks.ChildRunLinked)
	if err != nil {
		return err
	}
	if err := validateChildLinkEvent(event, session.RunStart{
		AgentID:     child.AgentID,
		RunID:       child.RunID,
		SessionID:   child.SessionID,
		ParentRunID: child.ParentRunID,
	}); err != nil {
		return err
	}
	if event.AgentID() != parent.AgentID {
		return errors.New("parent agent does not match stored run")
	}
	return nil
}

// validateRunStartedRecord decodes one run-started hook and compares every
// immutable fact supplied when the run was created.
func validateRunStartedRecord(record *runlog.Event, start session.RunStart) error {
	event, err := decodeHookRecord(record, hooks.RunStarted)
	if err != nil {
		return err
	}
	return validateRunStartedEvent(record, event, start)
}

// validateRunStartedEvent compares a decoded event with the run start that the
// same storage operation writes.
func validateRunStartedEvent(record *runlog.Event, event hooks.Event, start session.RunStart) error {
	if err := validateEventOwner(event, start.RunID, start.AgentID, start.SessionID); err != nil {
		return err
	}
	started := event.(*hooks.RunStartedEvent)
	if !record.Timestamp.Equal(start.StartedAt) {
		return errors.New("timestamp does not match run start")
	}
	if started.ParentRunID != start.ParentRunID {
		return errors.New("parent run id does not match run")
	}
	if started.PredecessorRunID != start.PredecessorRunID {
		return errors.New("predecessor run id does not match run")
	}
	if !maps.Equal(started.Labels, start.Labels) {
		return errors.New("labels do not match run")
	}
	return nil
}

// validateStoppedRecord decodes the record selected when an ended session
// prevents an accepted workflow from doing work.
func validateStoppedRecord(record *runlog.Event, start session.RunStart) error {
	event, err := decodeHookRecord(record, hooks.RunCompleted)
	if err != nil {
		return err
	}
	if err := validateEventOwner(event, start.RunID, start.AgentID, start.SessionID); err != nil {
		return err
	}
	completed := event.(*hooks.RunCompletedEvent)
	if !record.Timestamp.Equal(start.StartedAt) {
		return errors.New("timestamp does not match run start")
	}
	if completed.Status != "canceled" || completed.Phase != run.PhaseCanceled {
		return errors.New("ended-session record must cancel the run")
	}
	if completed.Cancellation.Reason != run.CancellationReasonSessionEnded {
		return errors.New("ended-session record has wrong cancellation reason")
	}
	if !maps.Equal(completed.Labels, start.Labels) {
		return errors.New("labels do not match run")
	}
	return nil
}

// validateChildLinkRecord checks the child identity stored on its parent run.
func validateChildLinkRecord(record *runlog.Event, start session.RunStart) error {
	event, err := decodeHookRecord(record, hooks.ChildRunLinked)
	if err != nil {
		return err
	}
	return validateChildLinkEvent(event, start)
}

// validateChildLinkEvent checks the parent call and child identity stored in a
// decoded child-link event.
func validateChildLinkEvent(event hooks.Event, start session.RunStart) error {
	linked := event.(*hooks.ChildRunLinkedEvent)
	if linked.RunID() != start.ParentRunID || linked.SessionID() != start.SessionID {
		return errors.New("parent identity does not match child run")
	}
	if linked.ChildRunID != start.RunID || linked.ChildAgentID != agent.Ident(start.AgentID) {
		return errors.New("child identity does not match run")
	}
	if linked.ToolName == "" || linked.ToolCallID == "" {
		return errors.New("parent tool name and call id are required")
	}
	return nil
}

// decodeHookRecord validates the common envelope before asking the hooks codec
// to validate and materialize the expected event type.
func decodeHookRecord(record *runlog.Event, expected hooks.EventType) (hooks.Event, error) {
	if err := storage.ValidateRunRecord(record); err != nil {
		return nil, err
	}
	if record.Type != expected {
		return nil, fmt.Errorf("record has type %q, want %q", record.Type, expected)
	}
	event, err := hooks.DecodeFromRecordInput(&runlog.ActivityInput{
		Type:        record.Type,
		EventKey:    record.EventKey,
		RunID:       record.RunID,
		AgentID:     record.AgentID,
		SessionID:   record.SessionID,
		TurnID:      record.TurnID,
		TimestampMS: record.Timestamp.UnixMilli(),
		Payload:     record.Payload,
	})
	if err != nil {
		return nil, err
	}
	return event, nil
}

// validateRecordOwner compares a non-hook record with stored run ownership.
func validateRecordOwner(record *runlog.Event, runID, agentID, sessionID string) error {
	if err := storage.ValidateRunRecord(record); err != nil {
		return err
	}
	if record.RunID != runID || string(record.AgentID) != agentID || record.SessionID != sessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	return nil
}

// validateEventOwner compares the typed hook envelope with stored run
// ownership.
func validateEventOwner(event hooks.Event, runID, agentID, sessionID string) error {
	if event.RunID() != runID || event.AgentID() != agentID || event.SessionID() != sessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	return nil
}

// terminalState maps a valid hook outcome onto its storage state and phase.
func terminalState(status string) (session.RunStatus, run.Phase) {
	switch status {
	case "success":
		return session.RunStatusCompleted, run.PhaseCompleted
	case "failed":
		return session.RunStatusFailed, run.PhaseFailed
	case "canceled":
		return session.RunStatusCanceled, run.PhaseCanceled
	default:
		return "", ""
	}
}
