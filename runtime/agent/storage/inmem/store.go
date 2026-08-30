// Package inmem provides the integrated runtime store used by tests and local
// examples.
//
// One mutex protects sessions, runs, checkpoints, and records. Lifecycle state
// and its matching records therefore become visible together. Production hosts
// must provide a durable implementation with the same transaction boundaries.
package inmem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

type (
	// Store keeps the complete runtime history in memory. It is safe for
	// concurrent use and also exposes host-owned session lifecycle operations.
	Store struct {
		mu sync.RWMutex

		sessions    map[string]session.Session
		runs        map[string]session.RunMeta
		suspensions map[string]session.RunSuspension
		lifecycle   map[string]lifecycleRecords
		purged      map[string]struct{}

		nextSequence   map[string]int64
		records        map[string][]*runlog.Event
		recordsByKey   map[string]map[string]*runlog.Event
		sessionRecords map[string][]*runlog.Event
	}

	// lifecycleRecords remembers which immutable record made each run state
	// visible. Exact retries must use these same keys.
	lifecycleRecords struct {
		start        string
		parentLink   string
		cancellation string
		suspension   string
		terminal     string
	}

	// sessionRunStartResult is the common private result used while the store
	// chooses the active-session or ended-session record.
	sessionRunStartResult struct {
		outcome      session.RunStartOutcome
		parentRecord storage.AppendResult
		runRecord    storage.AppendResult
	}
)

var _ storage.Store = (*Store)(nil)

// New returns an empty integrated store.
func New() *Store {
	return &Store{
		sessions:       make(map[string]session.Session),
		runs:           make(map[string]session.RunMeta),
		suspensions:    make(map[string]session.RunSuspension),
		lifecycle:      make(map[string]lifecycleRecords),
		purged:         make(map[string]struct{}),
		nextSequence:   make(map[string]int64),
		records:        make(map[string][]*runlog.Event),
		recordsByKey:   make(map[string]map[string]*runlog.Event),
		sessionRecords: make(map[string][]*runlog.Event),
	}
}

// CreateSession creates one active host-owned session. Repeating the same
// creation returns the existing active session.
//
//nolint:unparam // Host callers may use the returned canonical session.
func (s *Store) CreateSession(_ context.Context, sessionID string, createdAt time.Time) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	if createdAt.IsZero() {
		return session.Session{}, errors.New("created_at is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.purged[sessionID]; ok {
		return session.Session{}, session.ErrSessionPurged
	}
	if existing, ok := s.sessions[sessionID]; ok {
		if existing.Status == session.StatusEnded {
			return session.Session{}, session.ErrSessionEnded
		}
		return cloneSession(existing), nil
	}
	created := session.Session{ID: sessionID, Status: session.StatusActive, CreatedAt: createdAt.UTC()}
	s.sessions[sessionID] = created
	return cloneSession(created), nil
}

// LoadSession returns one host-owned session.
func (s *Store) LoadSession(_ context.Context, sessionID string) (session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.purged[sessionID]; ok {
		return session.Session{}, session.ErrSessionPurged
	}
	stored, ok := s.sessions[sessionID]
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	return cloneSession(stored), nil
}

// EndSession prevents future workflows from proceeding. Existing workflows
// may still store terminal records.
//
//nolint:unparam // Host callers may use the returned canonical session.
func (s *Store) EndSession(_ context.Context, sessionID string, endedAt time.Time) (session.Session, error) {
	if endedAt.IsZero() {
		return session.Session{}, errors.New("ended_at is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.purged[sessionID]; ok {
		return session.Session{}, session.ErrSessionPurged
	}
	stored, ok := s.sessions[sessionID]
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if stored.Status == session.StatusEnded {
		return cloneSession(stored), nil
	}
	at := endedAt.UTC()
	stored.Status = session.StatusEnded
	stored.EndedAt = &at
	s.sessions[sessionID] = stored
	return cloneSession(stored), nil
}

// PurgeSession permanently seals an ended session and removes its runs,
// checkpoints, and records.
func (s *Store) PurgeSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.purged[sessionID]; ok {
		return nil
	}
	stored, ok := s.sessions[sessionID]
	if !ok {
		return session.ErrSessionNotFound
	}
	if stored.Status == session.StatusActive {
		return session.ErrSessionActive
	}
	for _, meta := range s.runs {
		if meta.SessionID == sessionID && !session.IsTerminalRunStatus(meta.Status) {
			return session.ErrSessionHasActiveRuns
		}
	}
	s.purged[sessionID] = struct{}{}
	delete(s.sessions, sessionID)
	delete(s.sessionRecords, sessionID)
	for runID, meta := range s.runs {
		if meta.SessionID != sessionID {
			continue
		}
		delete(s.runs, runID)
		delete(s.suspensions, runID)
		delete(s.lifecycle, runID)
		delete(s.nextSequence, runID)
		delete(s.records, runID)
		delete(s.recordsByKey, runID)
	}
	return nil
}

// ListRunsBySession returns host-facing run metadata filtered by status.
//
//nolint:unparam // Host callers consume the returned run metadata.
func (s *Store) ListRunsBySession(_ context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error) {
	allowed := make(map[session.RunStatus]struct{}, len(statuses))
	for _, status := range statuses {
		allowed[status] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.purged[sessionID]; ok {
		return nil, session.ErrSessionPurged
	}
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, session.ErrSessionNotFound
	}
	result := make([]session.RunMeta, 0)
	for _, meta := range s.runs {
		if meta.SessionID != sessionID {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[meta.Status]; !ok {
				continue
			}
		}
		result = append(result, cloneRun(meta))
	}
	return result, nil
}

// StartRootRun atomically stores the root metadata and the record selected by
// the current session state.
func (s *Store) StartRootRun(_ context.Context, command storage.RootRunStart) (storage.RootRunStartResult, error) {
	if err := lifecycle.ValidateRootRunStart(command); err != nil {
		return contractResult(storage.RootRunStartResult{}, err)
	}
	result, err := s.startSessionRun(command.Run, false, nil, command.Started, command.Canceled)
	return contractResult(storage.RootRunStartResult{Outcome: result.outcome, Record: result.runRecord}, err)
}

// StartChildRun atomically stores the child metadata, parent link, and record
// selected by the current session state.
func (s *Store) StartChildRun(_ context.Context, command storage.ChildRunStart) (storage.ChildRunStartResult, error) {
	if err := lifecycle.ValidateChildRunStart(command); err != nil {
		return contractResult(storage.ChildRunStartResult{}, err)
	}
	result, err := s.startSessionRun(command.Run, true, command.ParentLinked, command.Started, command.Canceled)
	return contractResult(storage.ChildRunStartResult{
		Outcome: result.outcome, ParentRecord: result.parentRecord, RunRecord: result.runRecord,
	}, err)
}

// StartOneShotRun stores a sessionless run and its first record together.
func (s *Store) StartOneShotRun(_ context.Context, command storage.OneShotRunStart) (storage.OneShotRunStartResult, error) {
	return contractResult(s.startOneShotRun(command))
}

// startOneShotRun applies the command while StartOneShotRun assigns the public
// permanent-error category.
func (s *Store) startOneShotRun(command storage.OneShotRunStart) (storage.OneShotRunStartResult, error) {
	if err := lifecycle.ValidateOneShotRunStart(command); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.runs[command.Run.RunID]; ok {
		if !sameRunStart(existing, command.Run) || existing.StartOutcome != session.RunStartProceed {
			return storage.OneShotRunStartResult{}, session.ErrRunConflict
		}
		if s.lifecycle[command.Run.RunID].start != command.Started.EventKey {
			return storage.OneShotRunStartResult{}, session.ErrRunConflict
		}
		result, err := s.appendLocked(command.Started)
		return storage.OneShotRunStartResult{Record: result}, err
	}
	if err := s.checkNewRunRecordLocked(command.Started, command.Run); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	s.runs[command.Run.RunID] = newRunMeta(command.Run, session.RunStartProceed, session.RunStatusRunning)
	s.lifecycle[command.Run.RunID] = lifecycleRecords{start: command.Started.EventKey}
	result, err := s.appendLocked(command.Started)
	return storage.OneShotRunStartResult{Record: result}, err
}

// AppendRunRecord stores an ordinary record without changing lifecycle state.
func (s *Store) AppendRunRecord(_ context.Context, record *runlog.Event) (storage.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := lifecycle.ValidateOrdinaryRunRecord(record); err != nil {
		return storage.AppendResult{}, storage.NewContractError(err)
	}
	return contractResult(s.appendOrdinaryLocked(record))
}

// RecordRunCancellation atomically stores the first reason and its record.
func (s *Store) RecordRunCancellation(_ context.Context, command storage.RunCancellation) (storage.AppendResult, error) {
	return contractResult(s.recordRunCancellation(command))
}

// recordRunCancellation applies the write-once reason under the store lock.
func (s *Store) recordRunCancellation(command storage.RunCancellation) (storage.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.mutableRunLocked(command.RunID)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if err := lifecycle.ValidateRunCancellation(command, meta); err != nil {
		return storage.AppendResult{}, err
	}
	if meta.CancellationReason != "" && meta.CancellationReason != command.Reason {
		return storage.AppendResult{}, session.ErrRunCancellationConflict
	}
	if err := s.checkAppendLocked(command.Record); err != nil {
		return storage.AppendResult{}, err
	}
	keys := s.lifecycle[command.RunID]
	if keys.cancellation == "" && session.IsTerminalRunStatus(meta.Status) {
		return storage.AppendResult{}, session.ErrRunNotActive
	}
	if keys.cancellation != "" && keys.cancellation != command.Record.EventKey {
		return storage.AppendResult{}, session.ErrRunCancellationConflict
	}
	if meta.CancellationReason == "" {
		meta.CancellationReason = command.Reason
		meta.UpdatedAt = time.Now().UTC()
		s.runs[command.RunID] = meta
		keys.cancellation = command.Record.EventKey
		s.lifecycle[command.RunID] = keys
	}
	return s.appendLocked(command.Record)
}

// RecordRunSuspension atomically stores one checkpoint, its record, and the
// suspended terminal state.
func (s *Store) RecordRunSuspension(_ context.Context, command storage.RunSuspension) (storage.AppendResult, error) {
	return contractResult(s.recordRunSuspension(command))
}

// RepairRunSuspension stores the supplied checkpoint and record only when the
// run is still active. Any existing terminal state remains unchanged.
func (s *Store) RepairRunSuspension(_ context.Context, command storage.RunSuspension) (storage.RunRepairResult, error) {
	return contractResult(s.repairRunSuspension(command))
}

// recordRunSuspension stores the checkpoint and lifecycle change together.
func (s *Store) recordRunSuspension(command storage.RunSuspension) (storage.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.mutableRunLocked(command.RunID)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if err := lifecycle.ValidateRunSuspension(command, meta); err != nil {
		return storage.AppendResult{}, err
	}
	return s.recordRunSuspensionLocked(command, meta)
}

// repairRunSuspension chooses the existing terminal state or the supplied
// repair while holding the same lock used by ordinary workflow writes.
func (s *Store) repairRunSuspension(command storage.RunSuspension) (storage.RunRepairResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.mutableRunLocked(command.RunID)
	if err != nil {
		return storage.RunRepairResult{}, err
	}
	if err := lifecycle.ValidateRunSuspension(command, meta); err != nil {
		return storage.RunRepairResult{}, err
	}
	if session.IsTerminalRunStatus(meta.Status) {
		keys := s.lifecycle[command.RunID]
		stored, ok := s.suspensions[command.RunID]
		record := s.recordsByKey[command.RunID][command.Record.EventKey]
		if meta.Status == session.RunStatusSuspended &&
			keys.suspension == command.Record.EventKey &&
			record != nil && sameEvent(record, command.Record) &&
			ok && stored.ID == command.Suspension.ID && bytes.Equal(stored.Data, command.Suspension.Data) {
			return storage.RunRepairResult{
				Outcome: storage.RunRepairAlreadyStored,
				Status:  meta.Status,
				Record:  s.existingRecordResultLocked(record),
			}, nil
		}
		return storage.RunRepairResult{
			Outcome: storage.RunRepairDifferentTerminal,
			Status:  meta.Status,
		}, nil
	}
	if err := s.checkAppendLocked(command.Record); err != nil {
		return storage.RunRepairResult{}, err
	}
	result, err := s.recordRunSuspensionLocked(command, meta)
	return storage.RunRepairResult{
		Outcome: storage.RunRepairStored,
		Status:  session.RunStatusSuspended,
		Record:  result,
	}, err
}

// recordRunSuspensionLocked applies one exact workflow-owned suspension after
// the caller loads the run under the store lock.
func (s *Store) recordRunSuspensionLocked(command storage.RunSuspension, meta session.RunMeta) (storage.AppendResult, error) {
	if session.IsTerminalRunStatus(meta.Status) && meta.Status != session.RunStatusSuspended {
		return storage.AppendResult{}, session.ErrRunTerminalConflict
	}
	if stored, ok := s.suspensions[command.RunID]; ok &&
		(stored.ID != command.Suspension.ID || !bytes.Equal(stored.Data, command.Suspension.Data)) {
		return storage.AppendResult{}, session.ErrRunSuspensionConflict
	}
	if err := s.checkAppendLocked(command.Record); err != nil {
		return storage.AppendResult{}, err
	}
	keys := s.lifecycle[command.RunID]
	if keys.suspension != "" && keys.suspension != command.Record.EventKey {
		return storage.AppendResult{}, session.ErrRunSuspensionConflict
	}
	result, err := s.appendLocked(command.Record)
	if err != nil || !result.Inserted {
		return result, err
	}
	s.suspensions[command.RunID] = cloneSuspension(command.Suspension)
	meta.Status = session.RunStatusSuspended
	meta.UpdatedAt = time.Now().UTC()
	s.runs[command.RunID] = meta
	keys.suspension = command.Record.EventKey
	keys.terminal = command.Record.EventKey
	s.lifecycle[command.RunID] = keys
	return result, nil
}

// RecordRunTerminal atomically stores one terminal record and terminal state.
func (s *Store) RecordRunTerminal(_ context.Context, command storage.RunTerminal) (storage.AppendResult, error) {
	return contractResult(s.recordRunTerminal(command))
}

// RepairRunTerminal stores the supplied terminal record only when the run is
// still active. Any existing terminal state remains unchanged.
func (s *Store) RepairRunTerminal(_ context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	return contractResult(s.repairRunTerminal(command))
}

// recordRunTerminal stores the final event and status together.
func (s *Store) recordRunTerminal(command storage.RunTerminal) (storage.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.mutableRunLocked(command.RunID)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if err := lifecycle.ValidateRunTerminal(command, meta); err != nil {
		return storage.AppendResult{}, err
	}
	return s.recordRunTerminalLocked(command, meta)
}

// repairRunTerminal chooses the existing terminal state or the supplied
// repair while holding the same lock used by ordinary workflow writes.
func (s *Store) repairRunTerminal(command storage.RunTerminal) (storage.RunRepairResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.mutableRunLocked(command.RunID)
	if err != nil {
		return storage.RunRepairResult{}, err
	}
	if err := lifecycle.ValidateRunTerminal(command, meta); err != nil {
		return storage.RunRepairResult{}, err
	}
	if session.IsTerminalRunStatus(meta.Status) {
		keys := s.lifecycle[command.RunID]
		record := s.recordsByKey[command.RunID][command.Record.EventKey]
		if meta.Status == command.Status && keys.terminal == command.Record.EventKey &&
			record != nil && sameEvent(record, command.Record) {
			return storage.RunRepairResult{
				Outcome: storage.RunRepairAlreadyStored,
				Status:  meta.Status,
				Record:  s.existingRecordResultLocked(record),
			}, nil
		}
		return storage.RunRepairResult{
			Outcome: storage.RunRepairDifferentTerminal,
			Status:  meta.Status,
		}, nil
	}
	if err := s.checkAppendLocked(command.Record); err != nil {
		return storage.RunRepairResult{}, err
	}
	result, err := s.recordRunTerminalLocked(command, meta)
	return storage.RunRepairResult{
		Outcome: storage.RunRepairStored,
		Status:  command.Status,
		Record:  result,
	}, err
}

// recordRunTerminalLocked applies one exact workflow-owned terminal result
// after the caller loads the run under the store lock.
func (s *Store) recordRunTerminalLocked(command storage.RunTerminal, meta session.RunMeta) (storage.AppendResult, error) {
	if session.IsTerminalRunStatus(meta.Status) && meta.Status != command.Status {
		return storage.AppendResult{}, session.ErrRunTerminalConflict
	}
	if err := s.checkAppendLocked(command.Record); err != nil {
		return storage.AppendResult{}, err
	}
	keys := s.lifecycle[command.RunID]
	if keys.terminal != "" && keys.terminal != command.Record.EventKey {
		return storage.AppendResult{}, session.ErrRunTerminalConflict
	}
	result, err := s.appendLocked(command.Record)
	if err != nil || !result.Inserted {
		return result, err
	}
	meta.Status = command.Status
	meta.UpdatedAt = time.Now().UTC()
	s.runs[command.RunID] = meta
	keys.terminal = command.Record.EventKey
	s.lifecycle[command.RunID] = keys
	return result, nil
}

// LoadRun returns a copy of one run.
func (s *Store) LoadRun(_ context.Context, runID string) (session.RunMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta, ok := s.runs[runID]
	if !ok {
		return session.RunMeta{}, session.ErrRunNotFound
	}
	return cloneRun(meta), nil
}

// LoadRunSuspension returns a copy of one checkpoint.
func (s *Store) LoadRunSuspension(_ context.Context, runID string) (session.RunSuspension, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.runs[runID]; !ok {
		return session.RunSuspension{}, session.ErrRunNotFound
	}
	value, ok := s.suspensions[runID]
	if !ok {
		return session.RunSuspension{}, session.ErrRunSuspensionNotFound
	}
	return cloneSuspension(value), nil
}

// ListRunRecords returns one page ordered by the store-assigned run sequence.
func (s *Store) ListRunRecords(_ context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be greater than zero")
	}
	after, err := parseCursor(cursor)
	if err != nil {
		return runlog.Page{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.runs[runID]; !ok {
		return runlog.Page{}, session.ErrRunNotFound
	}
	return pageRecords(s.records[runID], after, limit), nil
}

// ListSessionRunRecords returns one page in session commit order.
func (s *Store) ListSessionRunRecords(_ context.Context, sessionID, cursor string, limit int) (runlog.Page, error) {
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be greater than zero")
	}
	after, err := parseCursor(cursor)
	if err != nil {
		return runlog.Page{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.purged[sessionID]; ok {
		return runlog.Page{}, session.ErrSessionPurged
	}
	if _, ok := s.sessions[sessionID]; !ok {
		return runlog.Page{}, session.ErrSessionNotFound
	}
	return pageRecords(s.sessionRecords[sessionID], after, limit), nil
}

// startSessionRun chooses the active or ended path while holding the same lock
// used by EndSession.
func (s *Store) startSessionRun(start session.RunStart, child bool, parent, started, canceled *runlog.Event) (sessionRunStartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, purged := s.purged[start.SessionID]; purged {
		return sessionRunStartResult{}, session.ErrSessionPurged
	}
	if parent != nil {
		if err := s.checkAppendLocked(parent); err != nil {
			return sessionRunStartResult{}, err
		}
	}
	if existing, ok := s.runs[start.RunID]; ok {
		if !sameRunStart(existing, start) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		if !sameStartRecordKeys(s.lifecycle[start.RunID], existing.StartOutcome, parent, started, canceled) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		return s.appendStartRecordsLocked(existing.StartOutcome, parent, started, canceled)
	}
	current, ok := s.sessions[start.SessionID]
	if !ok {
		return sessionRunStartResult{}, session.ErrSessionNotFound
	}
	if child {
		parentMeta, ok := s.runs[start.ParentRunID]
		if !ok {
			return sessionRunStartResult{}, session.ErrRunNotFound
		}
		if parentMeta.SessionID != start.SessionID {
			return sessionRunStartResult{}, session.ErrRunSessionMismatch
		}
	}
	outcome := session.RunStartProceed
	status := session.RunStatusRunning
	if current.Status == session.StatusEnded {
		outcome = session.RunStartStop
		status = session.RunStatusCanceled
	}
	selected := started
	if outcome == session.RunStartStop {
		selected = canceled
	}
	if err := s.checkNewRunRecordLocked(selected, start); err != nil {
		return sessionRunStartResult{}, err
	}
	s.runs[start.RunID] = newRunMeta(start, outcome, status)
	keys := lifecycleRecords{start: selected.EventKey}
	if parent != nil {
		keys.parentLink = parent.EventKey
	}
	if outcome == session.RunStartStop {
		keys.terminal = selected.EventKey
	}
	s.lifecycle[start.RunID] = keys
	return s.appendStartRecordsLocked(outcome, parent, started, canceled)
}

// appendStartRecordsLocked appends the exact record set selected by outcome.
func (s *Store) appendStartRecordsLocked(outcome session.RunStartOutcome, parent, started, canceled *runlog.Event) (sessionRunStartResult, error) {
	result := sessionRunStartResult{outcome: outcome}
	if parent != nil {
		stored, err := s.appendLocked(parent)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		result.parentRecord = stored
	}
	selected := started
	if outcome == session.RunStartStop {
		selected = canceled
	}
	stored, err := s.appendLocked(selected)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	result.runRecord = stored
	return result, nil
}

func (s *Store) checkNewRunRecordLocked(record *runlog.Event, start session.RunStart) error {
	if err := checkNewRunRecordOwner(record, start); err != nil {
		return err
	}
	if existing := s.recordsByKey[record.RunID][record.EventKey]; existing != nil && !sameEvent(existing, record) {
		return &runlog.EventConflictError{RunID: record.RunID, EventKey: record.EventKey}
	}
	return nil
}

// sameStartRecordKeys checks the records selected by the first durable start
// decision. The unused active or ended candidate is not part of that decision.
func sameStartRecordKeys(keys lifecycleRecords, outcome session.RunStartOutcome, parent, started, canceled *runlog.Event) bool {
	if parent != nil && keys.parentLink != parent.EventKey {
		return false
	}
	selected := started
	if outcome == session.RunStartStop {
		selected = canceled
	}
	return keys.start == selected.EventKey
}

// checkNewRunRecordOwner validates a possible first record without comparing
// it to stored records. Only the record selected by the session state takes
// part in exact retry conflict checks.
func checkNewRunRecordOwner(record *runlog.Event, start session.RunStart) error {
	if err := storage.ValidateRunRecord(record); err != nil {
		return err
	}
	if record.RunID != start.RunID {
		return errors.New("start record does not match run")
	}
	if string(record.AgentID) != start.AgentID || record.SessionID != start.SessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	return nil
}

func (s *Store) appendLocked(record *runlog.Event) (storage.AppendResult, error) {
	if err := s.checkAppendLocked(record); err != nil {
		return storage.AppendResult{}, err
	}
	return s.appendCheckedLocked(record), nil
}

// appendOrdinaryLocked rejects new work after a run stops while preserving
// exact retries of records that were stored while the run was active.
func (s *Store) appendOrdinaryLocked(record *runlog.Event) (storage.AppendResult, error) {
	if err := s.checkAppendLocked(record); err != nil {
		return storage.AppendResult{}, err
	}
	if existing := s.recordsByKey[record.RunID][record.EventKey]; existing != nil {
		return s.existingRecordResultLocked(existing), nil
	}
	if session.IsTerminalRunStatus(s.runs[record.RunID].Status) {
		return storage.AppendResult{}, session.ErrRunNotActive
	}
	return s.appendCheckedLocked(record), nil
}

// appendCheckedLocked assigns the next sequence to a record whose identity,
// owner, and retry key were already checked while the store lock is held.
func (s *Store) appendCheckedLocked(record *runlog.Event) storage.AppendResult {
	if existing := s.recordsByKey[record.RunID][record.EventKey]; existing != nil {
		return s.existingRecordResultLocked(existing)
	}
	sequence := s.nextSequence[record.RunID] + 1
	s.nextSequence[record.RunID] = sequence
	stored := cloneEvent(record)
	stored.ID = strconv.FormatInt(sequence, 10)
	if s.recordsByKey[record.RunID] == nil {
		s.recordsByKey[record.RunID] = make(map[string]*runlog.Event)
	}
	s.recordsByKey[record.RunID][record.EventKey] = stored
	s.records[record.RunID] = append(s.records[record.RunID], stored)
	if record.SessionID != "" {
		s.sessionRecords[record.SessionID] = append(s.sessionRecords[record.SessionID], stored)
	}
	return storage.AppendResult{ID: stored.ID, Inserted: true, SessionStatus: s.sessionStatusLocked(record.SessionID)}
}

// existingRecordResultLocked returns the original record identity for an
// exact retry without writing another history entry.
func (s *Store) existingRecordResultLocked(existing *runlog.Event) storage.AppendResult {
	return storage.AppendResult{ID: existing.ID, SessionStatus: s.sessionStatusLocked(existing.SessionID)}
}

func (s *Store) checkAppendLocked(record *runlog.Event) error {
	if err := storage.ValidateRunRecord(record); err != nil {
		return err
	}
	meta, ok := s.runs[record.RunID]
	if !ok {
		return session.ErrRunNotFound
	}
	if meta.AgentID != string(record.AgentID) || meta.SessionID != record.SessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	if record.SessionID != "" {
		if _, purged := s.purged[record.SessionID]; purged {
			return session.ErrSessionPurged
		}
		if _, ok := s.sessions[record.SessionID]; !ok {
			return session.ErrSessionNotFound
		}
	}
	if existing := s.recordsByKey[record.RunID][record.EventKey]; existing != nil && !sameEvent(existing, record) {
		return &runlog.EventConflictError{RunID: record.RunID, EventKey: record.EventKey}
	}
	return nil
}

func (s *Store) mutableRunLocked(runID string) (session.RunMeta, error) {
	meta, ok := s.runs[runID]
	if !ok {
		return session.RunMeta{}, session.ErrRunNotFound
	}
	if meta.SessionID != "" {
		if _, purged := s.purged[meta.SessionID]; purged {
			return session.RunMeta{}, session.ErrSessionPurged
		}
	}
	return meta, nil
}

func (s *Store) sessionStatusLocked(sessionID string) session.SessionStatus {
	if sessionID == "" {
		return ""
	}
	return s.sessions[sessionID].Status
}

func newRunMeta(start session.RunStart, outcome session.RunStartOutcome, status session.RunStatus) session.RunMeta {
	cancellationReason := ""
	if outcome == session.RunStartStop {
		cancellationReason = run.CancellationReasonSessionEnded
	}
	return session.RunMeta{
		AgentID: start.AgentID, RunID: start.RunID, SessionID: start.SessionID,
		ParentRunID: start.ParentRunID, Status: status, StartOutcome: outcome,
		StartedAt: start.StartedAt.UTC(), UpdatedAt: time.Now().UTC(), Labels: maps.Clone(start.Labels),
		CancellationReason: cancellationReason,
	}
}

func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(cursor)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid cursor %q", cursor)
	}
	return value, nil
}

func pageRecords(all []*runlog.Event, after, limit int) runlog.Page {
	if after >= len(all) {
		return runlog.Page{}
	}
	end := min(after+limit, len(all))
	page := make([]*runlog.Event, end-after)
	for index, record := range all[after:end] {
		page[index] = cloneEvent(record)
	}
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return runlog.Page{Events: page, NextCursor: next}
}

func sameRunStart(stored session.RunMeta, requested session.RunStart) bool {
	return stored.RunID == requested.RunID && stored.AgentID == requested.AgentID &&
		stored.SessionID == requested.SessionID && stored.ParentRunID == requested.ParentRunID &&
		stored.StartedAt.Equal(requested.StartedAt.UTC()) && maps.Equal(stored.Labels, requested.Labels)
}

func sameEvent(left, right *runlog.Event) bool {
	return left.EventKey == right.EventKey && left.RunID == right.RunID &&
		left.AgentID == right.AgentID && left.SessionID == right.SessionID &&
		left.TurnID == right.TurnID && left.Type == right.Type &&
		left.Timestamp.Equal(right.Timestamp) && bytes.Equal(left.Payload, right.Payload)
}

// contractResult marks every in-memory mutation failure as permanent. This
// store has no database or network failures, so none of its errors are
// temporary.
func contractResult[T any](result T, err error) (T, error) {
	if err != nil {
		return result, storage.NewContractError(err)
	}
	return result, nil
}

func cloneSession(value session.Session) session.Session {
	if value.EndedAt != nil {
		endedAt := *value.EndedAt
		value.EndedAt = &endedAt
	}
	return value
}

func cloneRun(value session.RunMeta) session.RunMeta {
	value.Labels = maps.Clone(value.Labels)
	return value
}

func cloneSuspension(value session.RunSuspension) session.RunSuspension {
	return session.RunSuspension{ID: value.ID, Data: bytes.Clone(value.Data)}
}

func cloneEvent(value *runlog.Event) *runlog.Event {
	copy := *value
	copy.Payload = bytes.Clone(value.Payload)
	return &copy
}
