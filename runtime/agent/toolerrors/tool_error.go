// Package toolerrors provides structured error types for tool invocation failures.
// ToolError preserves error chains and supports errors.Is/As while maintaining
// serialization compatibility for agent-as-tool scenarios.
package toolerrors

import (
	"errors"
	"fmt"
)

// ToolError represents a structured tool failure that preserves message and causal
// context while still implementing the standard error interface. Tool errors may be
// nested via Cause to retain rich diagnostics across retries and agent-as-tool hops.
type ToolError struct {
	// Message is the human-readable summary of the failure.
	Message string
	// Cause links to the underlying tool error, enabling error chains with errors.Is/As.
	Cause *ToolError
}

// New constructs a ToolError with the provided message. Use when the failure does not
// wrap an underlying error but still requires structured reporting.
func New(message string) *ToolError {
	if message == "" {
		message = "tool error"
	}
	return &ToolError{Message: message}
}

// NewWithCause constructs a ToolError that wraps an underlying error. The cause is
// converted into a ToolError chain so error metadata survives serialization while still
// supporting errors.Is/As through Unwrap.
func NewWithCause(message string, cause error) *ToolError {
	if message == "" && cause != nil {
		message = cause.Error()
	}
	return &ToolError{
		Message: message,
		Cause:   FromError(cause),
	}
}

// FromError converts an arbitrary error into a ToolError chain.
func FromError(err error) *ToolError {
	if err == nil {
		return nil
	}
	var te *ToolError
	if errors.As(err, &te) {
		return te
	}
	return &ToolError{
		Message: err.Error(),
		Cause:   FromError(errors.Unwrap(err)),
	}
}

// Errorf formats according to a format specifier and returns the string as a ToolError.
func Errorf(format string, args ...any) *ToolError {
	return New(fmt.Sprintf(format, args...))
}

// Error implements the error interface.
func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap returns the underlying tool error to support errors.Is/As.
func (e *ToolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
