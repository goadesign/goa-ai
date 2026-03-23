// Package runtime records cancellation provenance before signaling the engine.
//
// Durable repair paths use the stored reason to rebuild canonical terminal run
// outcomes after restarts instead of inferring intent from a bare
// context.Canceled error.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

const runMetaCancellationReason = "cancellation_reason"

type (
	// CancelRequest describes an explicit runtime-owned cancellation request.
	//
	// Contract:
	// - RunID and Reason are required.
	// - Reason should use the canonical run.CancellationReason* constants.
	CancelRequest struct {
		// RunID identifies the run to cancel.
		RunID string
		// Reason records who or what initiated the cancellation.
		Reason string
	}
)

// recordRunCancellation persists cancellation provenance on an active run before
// the engine is signaled so terminal repair can later reconstruct the canonical
// outcome. It returns the pre-write run metadata when it changed durable state
// so callers can roll the write back if cancellation never reaches the engine.
func (r *Runtime) recordRunCancellation(
	ctx context.Context,
	req CancelRequest,
) (session.RunMeta, bool, error) {
	if r.SessionStore == nil {
		return session.RunMeta{}, false, nil
	}
	meta, err := r.SessionStore.LoadRun(ctx, req.RunID)
	if err != nil {
		if errors.Is(err, session.ErrRunNotFound) {
			return session.RunMeta{}, false, nil
		}
		return session.RunMeta{}, false, err
	}
	if isTerminalSessionRunStatus(meta.Status) {
		return session.RunMeta{}, false, nil
	}
	existing, err := cancellationFromRunMetadata(meta.Metadata)
	if err != nil {
		return session.RunMeta{}, false, err
	}
	if existing != nil {
		return meta, false, nil
	}
	next := meta
	metadata := cloneMetadata(meta.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata[runMetaCancellationReason] = req.Reason
	next.Metadata = metadata
	if err := r.SessionStore.UpsertRun(ctx, next); err != nil {
		return session.RunMeta{}, false, err
	}
	return meta, true, nil
}

// rollbackRunCancellation removes a provisional cancellation reason when the
// engine rejected the cancel request before the run reached a terminal state.
func (r *Runtime) rollbackRunCancellation(ctx context.Context, previous session.RunMeta, req CancelRequest) error {
	if r.SessionStore == nil {
		return nil
	}
	current, err := r.SessionStore.LoadRun(ctx, req.RunID)
	if err != nil {
		if errors.Is(err, session.ErrRunNotFound) {
			return nil
		}
		return err
	}
	if isTerminalSessionRunStatus(current.Status) {
		return nil
	}
	currentCancellation, err := cancellationFromRunMetadata(current.Metadata)
	if err != nil {
		return err
	}
	if currentCancellation == nil || currentCancellation.Reason != req.Reason {
		return nil
	}
	current.Metadata = cloneMetadata(previous.Metadata)
	return r.SessionStore.UpsertRun(ctx, current)
}

// loadRunCancellation loads the stored cancellation provenance for the run when
// one was recorded before cancellation.
func (r *Runtime) loadRunCancellation(ctx context.Context, runID string) (*run.Cancellation, error) {
	if r.SessionStore == nil {
		return nil, nil
	}
	meta, err := r.SessionStore.LoadRun(ctx, runID)
	if err != nil {
		if errors.Is(err, session.ErrRunNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return cancellationFromRunMetadata(meta.Metadata)
}

// cancellationFromRunMetadata decodes the canonical cancellation payload from
// stored run metadata.
func cancellationFromRunMetadata(metadata map[string]any) (*run.Cancellation, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	raw, ok := metadata[runMetaCancellationReason]
	if !ok {
		return nil, nil
	}
	reason, ok := raw.(string)
	if !ok || reason == "" {
		return nil, fmt.Errorf("invalid %s metadata", runMetaCancellationReason)
	}
	return &run.Cancellation{
		Reason: reason,
	}, nil
}

// isTerminalSessionRunStatus reports whether the session store state no longer
// accepts new cancellation intent for the run.
func isTerminalSessionRunStatus(status session.RunStatus) bool {
	switch status {
	case session.RunStatusPending, session.RunStatusRunning, session.RunStatusPaused:
		return false
	case session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled:
		return true
	}
	panic("runtime: unsupported session run status: " + string(status))
}
