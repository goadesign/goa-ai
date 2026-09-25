package model

// Request validation errors identify model requests rejected by application-side
// validation, before a model provider accepts them. The runtime keeps these
// failures terminal without attributing them to a provider or requesting a model
// correction. Adapters must mark only known request validation failures, not
// network, observer, or provider errors.

import "errors"

type (
	// RequestValidationError reports that a model request failed local validation.
	// Retrying the unchanged request cannot succeed. The original cause remains
	// available for diagnostics and errors.Is/As; no provider facts are implied.
	RequestValidationError struct {
		cause error
	}
)

// ErrRequestByteCapacity reports that the complete request exceeds a known local
// byte allowance, measured before remote inference dispatch. It is a terminal
// RequestValidationError; adapters may wrap it to include the measured size.
// History compression may select fewer optional older turns, but cannot discard
// the newest turn or required summary evidence. Do not infer this error from
// image presence, token counts, remote status codes, or unknown provider limits.
var ErrRequestByteCapacity = NewRequestValidationError(errors.New("model request exceeds local byte allowance"))

// NewRequestValidationError marks a known local model-request rejection. cause
// must be non-nil and must describe the validation that rejected the request.
func NewRequestValidationError(cause error) *RequestValidationError {
	if cause == nil {
		panic("model: request validation error requires a cause")
	}
	return &RequestValidationError{cause: cause}
}

// Error returns the complete original diagnostic without replacing its text.
func (e *RequestValidationError) Error() string {
	return e.cause.Error()
}

// Unwrap exposes the original validation failure.
func (e *RequestValidationError) Unwrap() error {
	return e.cause
}
