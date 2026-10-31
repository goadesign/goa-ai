// Package model keeps reported token counts when a provider invocation fails
// after completing some work. Counts describe the whole invocation so observers
// replace streamed deltas instead of counting the same tokens twice.
package model

import (
	"errors"
	"fmt"
)

type (
	// usageError adds validated counts without changing the original failure.
	usageError struct {
		cause      error
		usage      TokenUsage
		incomplete bool
	}
)

// RetainUsage attaches the total reported usage of a failed invocation to err.
// The original error remains available through errors.Is and errors.As. Counts
// must include all completed provider requests, including earlier search steps.
// Invalid counts add a validation error and are never exposed as valid usage.
// Passing nil is a programming error: successful calls return Response.Usage.
func RetainUsage(err error, usage TokenUsage) error {
	return retainUsageWithCompleteness(err, usage, false)
}

// RetainIncompleteUsage attaches partial or incomplete observed token usage of a
// failed invocation to err (for example, when a multi-round search fails after
// an uncounted round or a response reports only partial counters). Rate limiters
// charge overruns from incomplete usage without refunding unknown work.
func RetainIncompleteUsage(err error, usage TokenUsage) error {
	return retainUsageWithCompleteness(err, usage, true)
}

// UsageFromError returns an owned copy of reported invocation totals retained
// by RetainUsage, RetainIncompleteUsage, or an OutputValidationError. It returns
// nil when no validated counts are available. These totals replace, rather than
// add to, stream deltas.
func UsageFromError(err error) *TokenUsage {
	usage, _ := usageFromError(err)
	return usage
}

// UsageIncompleteFromError reports whether the invocation usage retained on err
// represents partial or incomplete token counts.
func UsageIncompleteFromError(err error) bool {
	_, incomplete := usageFromError(err)
	return incomplete
}

// Error returns the wrapped cause message unchanged.
func (e *usageError) Error() string {
	return e.cause.Error()
}

// Unwrap returns the wrapped cause.
func (e *usageError) Unwrap() error {
	return e.cause
}

// retainUsageWithCompleteness attaches validated usage and its completeness
// flag to err while preserving OutputValidationError root identity when present.
func retainUsageWithCompleteness(err error, usage TokenUsage, incomplete bool) error {
	if err == nil {
		panic("model: retaining failed usage requires an error")
	}
	if invalid := validateTokenUsage(usage); invalid != nil {
		return errors.Join(err, fmt.Errorf("model: invalid failed invocation usage: %w", invalid))
	}
	// A complete output rejection keeps its concrete type for transports that
	// distinguish it from a rejection joined with another operation failure.
	// Keep the original error in the chain and leave its evidence unchanged.
	if rejected, ok := err.(*OutputValidationError); ok { //nolint:errorlint // Only an exact rejection can retain this classification.
		owned := *rejected
		owned.cause = rejected
		owned.usage = &usage
		owned.usageIncomplete = incomplete
		return &owned
	}
	return &usageError{cause: err, usage: usage, incomplete: incomplete}
}

// usageFromError walks err in order so an outer retained total supersedes
// earlier nested counts and returns both the cloned usage and its completeness.
func usageFromError(err error) (*TokenUsage, bool) {
	switch failure := err.(type) { //nolint:errorlint // Walk in order so an outer total supersedes earlier nested counts.
	case *usageError:
		usage := failure.usage
		return &usage, failure.incomplete
	case *OutputValidationError:
		if usage := failure.Usage(); usage != nil {
			return usage, failure.usageIncomplete
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return usageFromError(wrapped.Unwrap())
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if usage, incomplete := usageFromError(cause); usage != nil {
				return usage, incomplete
			}
		}
	}
	return nil, false
}
