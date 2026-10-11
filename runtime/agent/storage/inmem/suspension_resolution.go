// Package inmem chooses closure or continuation under the same lock used by
// starts, session ending, and deletion. Closing continuation preserves every
// historical record and the original checkpoint bytes.
package inmem

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// RelinquishRunSuspension stores a cancellation resolution only for the exact
// unresolved root suspension. A competing admitted successor is returned intact.
func (s *Store) RelinquishRunSuspension(_ context.Context, command storage.RunSuspensionRelinquishment) (session.SuspensionResolution, error) {
	return contractResult(s.relinquishRunSuspension(command))
}

// relinquishRunSuspension validates the exact root and checkpoint before
// selecting a resolution under the lock shared with starts and deletion.
func (s *Store) relinquishRunSuspension(command storage.RunSuspensionRelinquishment) (session.SuspensionResolution, error) {
	if command.RunID == "" || command.SuspensionID == "" || command.Reason == "" ||
		!utf8.ValidString(command.RunID) || !utf8.ValidString(command.SuspensionID) || !utf8.ValidString(command.Reason) {
		return nil, errors.New("suspension relinquishment requires valid run id, suspension id, and reason")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, exists := s.runs[command.RunID]
	if !exists {
		return nil, session.ErrRunNotFound
	}
	if meta.Status != session.RunStatusSuspended || meta.ParentRunID != "" {
		return nil, session.ErrRunConflict
	}
	saved, exists := s.suspensions[command.RunID]
	if !exists {
		return nil, session.ErrRunSuspensionNotFound
	}
	if saved.ID != command.SuspensionID {
		return nil, session.ErrRunSuspensionConflict
	}
	if meta.CancellationReason != "" && meta.CancellationReason != command.Reason {
		return nil, session.ErrRunCancellationConflict
	}
	switch resolved := meta.SuspensionResolution.(type) {
	case session.ContinuedSuspension:
		return resolved, nil
	case session.RelinquishedSuspension:
		if resolved.Reason != command.Reason {
			return nil, session.ErrRunCancellationConflict
		}
		return resolved, nil
	case nil:
		closed := session.RelinquishedSuspension{Reason: command.Reason, ResolvedAt: time.Now().UTC().Truncate(time.Millisecond)}
		meta.SuspensionResolution = closed
		meta.UpdatedAt = closed.ResolvedAt
		s.runs[command.RunID] = meta
		return closed, nil
	default:
		return nil, errors.New("stored suspension has an invalid resolution")
	}
}
