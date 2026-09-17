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
		cause error
		usage TokenUsage
	}
)

// RetainUsage attaches the total reported usage of a failed invocation to err.
// The original error remains available through errors.Is and errors.As. Counts
// must include all completed provider requests, including earlier search steps.
// Invalid counts add a validation error and are never exposed as valid usage.
// Passing nil is a programming error: successful calls return Response.Usage.
func RetainUsage(err error, usage TokenUsage) error {
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
		return &owned
	}
	return &usageError{cause: err, usage: usage}
}

// UsageFromError returns an owned copy of reported invocation totals retained
// by RetainUsage or an OutputValidationError. It returns nil when no validated
// counts are available. These totals replace, rather than add to, stream deltas.
func UsageFromError(err error) *TokenUsage {
	switch failure := err.(type) { //nolint:errorlint // Walk in order so an outer total supersedes earlier nested counts.
	case *usageError:
		usage := failure.usage
		return &usage
	case *OutputValidationError:
		if usage := failure.Usage(); usage != nil {
			return usage
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return UsageFromError(wrapped.Unwrap())
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if usage := UsageFromError(cause); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func (e *usageError) Error() string {
	return e.cause.Error()
}

func (e *usageError) Unwrap() error {
	return e.cause
}
