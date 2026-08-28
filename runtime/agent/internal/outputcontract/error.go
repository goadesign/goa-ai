// Package outputcontract reports model, planner, or tool output that the agent
// runtime cannot accept. Generated tool-call validation and planners may supply
// bounded replacement guidance; rejections without safe guidance end the run.
package outputcontract

import (
	"strings"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// Origin identifies the component whose completed output failed validation.
	Origin string

	// Error reports completed output that broke its contract.
	Error struct {
		cause      error
		origin     Origin
		correction string
		message    *model.Message
	}
)

const (
	// MaxCorrectionBytes bounds framework- or planner-authored correction
	// guidance carried between workflow activities.
	MaxCorrectionBytes = correction.MaxBytes

	// OriginModel identifies rejected model output.
	OriginModel Origin = "model"

	// OriginPlanner identifies a rejected planner result.
	OriginPlanner Origin = "planner"

	// OriginTool identifies a rejected tool execution result.
	OriginTool Origin = "tool"
)

// NewWithOrigin records why one known output boundary rejected a completed
// value.
func NewWithOrigin(cause error, origin Origin) *Error {
	if cause == nil {
		panic("outputcontract: error requires a cause")
	}
	if origin != OriginModel && origin != OriginPlanner && origin != OriginTool {
		panic("outputcontract: error requires a valid origin")
	}
	return &Error{cause: cause, origin: origin}
}

// NewRecoverableModelOutput records a completed model answer that the planner
// rejected and the model can replace using the supplied guidance.
func NewRecoverableModelOutput(cause error, message *model.Message, correction string) *Error {
	if cause == nil {
		panic("outputcontract: error requires a cause")
	}
	if message == nil {
		panic("outputcontract: recoverable model output requires the rejected message")
	}
	if strings.TrimSpace(correction) == "" {
		panic("outputcontract: recoverable model output requires correction guidance")
	}
	if len(correction) > MaxCorrectionBytes {
		panic("outputcontract: correction guidance exceeds workflow boundary limit")
	}
	return &Error{
		cause:      cause,
		origin:     OriginModel,
		correction: correction,
		message:    message,
	}
}

// Error returns a stable summary without rendering the rejected output or its
// validation cause. Typed accessors retain the origin and bounded correction.
func (e *Error) Error() string {
	return "completed output does not meet its contract"
}

// Unwrap returns the original rejection error.
func (e *Error) Unwrap() error {
	return e.cause
}

// Origin identifies the component whose output failed validation.
func (e *Error) Origin() Origin {
	return e.origin
}

// Correction returns bounded guidance for replacing a rejected model answer.
// Empty means the rejection is terminal.
func (e *Error) Correction() string {
	return e.correction
}

// ModelMessage returns the exact completed answer rejected by the planner.
// It is nil for terminal output errors.
func (e *Error) ModelMessage() *model.Message {
	return e.message
}
