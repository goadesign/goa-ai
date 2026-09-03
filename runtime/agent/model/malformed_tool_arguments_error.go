// This file defines the error marker used when a provider returns tool
// arguments that are not valid JSON. Provider adapters add the marker at the
// exact JSON parsing failure so unrelated translation failures remain terminal.

package model

type malformedToolArgumentsError struct {
	cause error
}

// NewMalformedToolArgumentsError marks a provider-returned tool argument value
// that is not valid JSON. cause must be the exact parsing failure.
func NewMalformedToolArgumentsError(cause error) error {
	if cause == nil {
		panic("model: malformed tool arguments require a cause")
	}
	return &malformedToolArgumentsError{cause: cause}
}

// Error describes the contract failure without rendering provider argument
// bytes or adapter diagnostics.
func (e *malformedToolArgumentsError) Error() string {
	return "model tool arguments are not valid JSON"
}

// Unwrap preserves the provider adapter's private cause for in-process
// diagnostics.
func (e *malformedToolArgumentsError) Unwrap() error {
	return e.cause
}

// modelRecoveryCorrection supplies the fixed replacement instruction consumed
// by model-invocation recovery.
func (e *malformedToolArgumentsError) modelRecoveryCorrection() string {
	return malformedToolArgumentsCorrection
}
