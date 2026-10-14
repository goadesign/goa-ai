// Package runtime returns a selected operation error only after the service
// workflow's selected tool fails. Completion transport carries the message and
// restores the unchanged diagnostic before writing the terminal event.
package runtime

import "goa.design/goa-ai/runtime/agent/toolerrors"

type (
	selectedOperationError struct {
		cause *toolerrors.ToolError
	}
)

// Error preserves the service workflow's existing diagnostic text.
func (e *selectedOperationError) Error() string {
	return "registered tool failed: " + e.cause.Error()
}

// Unwrap preserves the tool error and its existing cause chain for callers.
func (e *selectedOperationError) Unwrap() error {
	return e.cause
}
