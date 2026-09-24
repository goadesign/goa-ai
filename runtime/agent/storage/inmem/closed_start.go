// Package inmem selects a closed run's original records for the same accepted
// request. The store lock covers the request binding, selected records and
// closing state; selection never appends records or changes timestamps.
package inmem

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

// checkStartDigestLocked requires the request bound by the first atomic start.
// A missing binding cannot prove that another execution accepted the same input.
func (s *Store) checkStartDigestLocked(runID string, digest [32]byte) error {
	if err := s.checkStartKindLocked(runID, engineRunStart); err != nil {
		return err
	}
	keys := s.lifecycle[runID]
	if keys.requestDigest == ([32]byte{}) {
		return errors.New("stored run has no accepted request digest")
	}
	if keys.requestDigest != digest {
		return session.ErrRunConflict
	}
	return nil
}

// checkStartKindLocked rejects another execution owner before any record can be
// selected or written. Missing or invalid metadata never identifies an owner.
func (s *Store) checkStartKindLocked(runID string, kind runStartKind) error {
	keys, ok := s.lifecycle[runID]
	if !ok || (keys.startKind != engineRunStart && keys.startKind != synchronousRunStart) {
		return errors.New("stored run has no valid start kind")
	}
	if keys.startKind != kind {
		return session.ErrRunConflict
	}
	if kind == synchronousRunStart && keys.requestDigest != ([32]byte{}) {
		return errors.New("stored synchronous run has an engine request digest")
	}
	return nil
}

// closedStartLocked validates the original start and closing facts before
// returning their committed IDs. The caller has validated the complete proposed
// command and its request digest while holding the store lock.
func (s *Store) closedStartLocked(meta session.RunMeta, proposed session.RunStart, parent, started, canceled *runlog.Event) (sessionRunStartResult, error) {
	keys := s.lifecycle[meta.RunID]
	if meta.StartOutcome != session.RunStartProceed && meta.StartOutcome != session.RunStartStop {
		return sessionRunStartResult{}, errors.New("stored run has invalid start outcome")
	}
	if meta.SessionID != "" {
		if _, ok := s.sessions[meta.SessionID]; !ok {
			return sessionRunStartResult{}, session.ErrSessionNotFound
		}
	}
	original, err := s.selectedStartRecordLocked(meta.RunID, keys.start)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	if err := lifecycle.ValidateStoredRunStart(original, meta); err != nil {
		return sessionRunStartResult{}, fmt.Errorf("stored run start: %w", err)
	}
	// The stored event must prove the original time. Only after that proof can
	// a new execution select it without using its newly proposed time.
	proposed.StartedAt = original.Timestamp
	if !sameRunStart(meta, proposed) || !sameStartCandidate(original, started) {
		return sessionRunStartResult{}, session.ErrRunConflict
	}
	result := sessionRunStartResult{
		outcome: meta.StartOutcome, runStatus: meta.Status,
		started: s.existingRecordResultLocked(original),
	}
	if parent != nil {
		parentMeta, ok := s.runs[meta.ParentRunID]
		if !ok {
			return sessionRunStartResult{}, session.ErrRunNotFound
		}
		link, err := s.selectedStartRecordLocked(meta.ParentRunID, keys.parentLink)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if err := lifecycle.ValidateStoredChildLink(link, parentMeta, meta); err != nil {
			return sessionRunStartResult{}, fmt.Errorf("stored child link: %w", err)
		}
		if !sameStartCandidate(link, parent) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		result.parentRecord = s.existingRecordResultLocked(link)
	} else if keys.parentLink != "" {
		return sessionRunStartResult{}, session.ErrRunConflict
	}
	closing, err := s.selectedStartRecordLocked(meta.RunID, keys.terminal)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	if meta.Status == session.RunStatusSuspended {
		checkpoint, ok := s.suspensions[meta.RunID]
		if !ok || keys.suspension != keys.terminal {
			return sessionRunStartResult{}, session.ErrRunSuspensionNotFound
		}
		err = lifecycle.ValidateRunSuspension(storage.RunSuspension{
			RunID: meta.RunID, Suspension: checkpoint, Record: closing,
		}, meta)
	} else {
		err = lifecycle.ValidateRunTerminal(storage.RunTerminal{
			RunID: meta.RunID, Status: meta.Status, Record: closing,
		}, meta)
	}
	if err != nil {
		return sessionRunStartResult{}, fmt.Errorf("stored run closing record: %w", err)
	}
	if meta.StartOutcome == session.RunStartStop {
		if canceled == nil || meta.SessionID == "" || meta.Status != session.RunStatusCanceled ||
			!closing.Timestamp.Equal(meta.StartedAt) || !sameStartCandidate(closing, canceled) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		result.canceled = s.existingRecordResultLocked(closing)
	}
	return result, nil
}

// selectedStartRecordLocked requires an existing keyed record and its committed
// ID. Missing records are corruption, never permission to recreate history.
func (s *Store) selectedStartRecordLocked(runID, key string) (*runlog.Event, error) {
	record := s.recordsByKey[runID][key]
	if key == "" || record == nil || record.ID == "" {
		return nil, errors.New("stored run is missing a required lifecycle record")
	}
	if record.RunID != runID || record.EventKey != key {
		return nil, errors.New("stored lifecycle record does not match its selected key")
	}
	return record, nil
}

// sameStartCandidate compares every immutable event field except the new
// execution's proposed occurrence time. The caller must first validate the
// selected original record against the stored run.
func sameStartCandidate(original, proposed *runlog.Event) bool {
	candidate := *proposed
	candidate.Timestamp = original.Timestamp
	return sameEvent(original, &candidate)
}
