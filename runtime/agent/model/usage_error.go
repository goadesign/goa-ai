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
	return &usageError{cause: err, usage: usage}
}

// UsageFromError returns an owned copy of reported invocation totals retained
// by RetainUsage or an OutputValidationError. It returns nil when no validated
// counts are available. These totals replace, rather than add to, stream deltas.
func UsageFromError(err error) *TokenUsage {
	var failed *usageError
	if errors.As(err, &failed) {
		usage := failed.usage
		return &usage
	}
	var rejected *OutputValidationError
	if errors.As(err, &rejected) {
		return rejected.Usage()
	}
	return nil
}

func (e *usageError) Error() string {
	return e.cause.Error()
}

func (e *usageError) Unwrap() error {
	return e.cause
}
