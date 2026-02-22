// Package runtime defines typed error contracts exposed to callers.
//
// This file contains await-resume delivery errors returned by Provide*
// operations. These errors let service layers distinguish stale/closed runs
// from internal dependency failures without inspecting backend-specific text.
package runtime

import "errors"

type (
	// RunNotAwaitableReason classifies why a run cannot accept await-resume input.
	RunNotAwaitableReason string

	// RunNotAwaitableError reports that a run cannot accept clarification answers,
	// confirmation decisions, or external tool results.
	//
	// Reason indicates whether the run is unknown or already completed. Cause
	// carries the underlying engine error.
	RunNotAwaitableError struct {
		// RunID identifies the rejected run.
		RunID string
		// Reason describes why the run rejected await-resume input.
		Reason RunNotAwaitableReason
		// Cause stores the underlying engine-level error.
		Cause error
	}
)

const (
	// RunNotAwaitableUnknownRun indicates no workflow exists for RunID.
	RunNotAwaitableUnknownRun RunNotAwaitableReason = "unknown_run"
	// RunNotAwaitableCompletedRun indicates the workflow already reached a terminal state.
	RunNotAwaitableCompletedRun RunNotAwaitableReason = "completed_run"
)

var (
	// ErrRunNotAwaitable matches all RunNotAwaitableError instances via errors.Is.
	ErrRunNotAwaitable = errors.New("run is not awaitable")
)

// Error returns a stable, human-readable classification for logs.
func (e *RunNotAwaitableError) Error() string {
	if e == nil {
		return ErrRunNotAwaitable.Error()
	}
	if e.RunID == "" {
		return "run is not awaitable"
	}
	return "run " + e.RunID + " is not awaitable"
}

// Unwrap exposes the engine cause.
func (e *RunNotAwaitableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is allows errors.Is(err, ErrRunNotAwaitable) classification.
func (e *RunNotAwaitableError) Is(target error) bool {
	return target == ErrRunNotAwaitable
}

// IsRunNotAwaitable reports whether err classifies as run-not-awaitable.
func IsRunNotAwaitable(err error) bool {
	return errors.Is(err, ErrRunNotAwaitable)
}

// AsRunNotAwaitable extracts a typed run-not-awaitable error.
func AsRunNotAwaitable(err error) (*RunNotAwaitableError, bool) {
	var typed *RunNotAwaitableError
	if !errors.As(err, &typed) {
		return nil, false
	}
	return typed, true
}
