// Package outputvalidation carries private provider-adapter classification from
// the check that rejects provider output to the adapter's model boundary.
package outputvalidation

import (
	"errors"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// outputError retains one provider translation cause and its closed framework
	// category inside the adapter process.
	outputError struct {
		kind  model.OutputValidationKind
		cause error
	}
)

// New marks the exact provider translation check that rejected output.
func New(kind model.OutputValidationKind, cause error) error {
	return &outputError{kind: kind, cause: cause}
}

// RequiredKind returns the category attached by a provider translation check.
// It panics when translation returned an unclassified rejection because that
// violates the adapter's internal contract.
func RequiredKind(err error) model.OutputValidationKind {
	var outputErr *outputError
	if !errors.As(err, &outputErr) {
		panic("outputvalidation: provider output rejection is missing its category")
	}
	return outputErr.kind
}

// Error returns the private adapter cause for in-process diagnostics.
func (e *outputError) Error() string {
	return e.cause.Error()
}

// Unwrap preserves the original adapter cause for errors.Is and errors.As.
func (e *outputError) Unwrap() error {
	return e.cause
}
