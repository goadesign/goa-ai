// Package mcp defines errors that callers can inspect without parsing text.
package mcp

import (
	"errors"
	"fmt"
	"strings"

	toolcontent "goa.design/goa-ai/runtime/content"
)

type (
	// OutcomeUnknownError means a sent tool request lost its usable response.
	// The operation may have completed; issuing it again can repeat its effects.
	OutcomeUnknownError struct {
		cause error
	}

	// MalformedResponseError reports an MCP response with missing or invalid fields.
	MalformedResponseError struct {
		cause error
	}

	// InternalError reports a bug in the MCP client.
	InternalError struct {
		cause error
	}

	// ToolExecutionError reports an MCP tools/call response whose isError flag
	// says the remote tool rejected or failed the call.
	ToolExecutionError struct {
		// Response contains the text and structured data returned by the tool.
		Response CallResponse
	}
)

// NewOutcomeUnknownError retains the failure that prevented proof of completion.
func NewOutcomeUnknownError(cause error) *OutcomeUnknownError {
	if cause == nil {
		panic("mcp: unknown outcome requires a cause")
	}
	return &OutcomeUnknownError{cause: cause}
}

// Error describes the missing completion evidence for the sent operation.
func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("MCP tool outcome is unknown: %v", e.cause)
}

// Unwrap returns the transport or response failure.
func (e *OutcomeUnknownError) Unwrap() error { return e.cause }

// NewMalformedResponseError wraps a response decoding or shape failure.
func NewMalformedResponseError(cause error) *MalformedResponseError {
	if cause == nil {
		panic("mcp: malformed response error requires a cause")
	}
	return &MalformedResponseError{cause: cause}
}

// Error implements error.
func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("malformed MCP response: %v", e.cause)
}

// Unwrap returns the response failure.
func (e *MalformedResponseError) Unwrap() error {
	return e.cause
}

// NewInternalError wraps a bug in the MCP client.
func NewInternalError(cause error) *InternalError {
	if cause == nil {
		panic("mcp: internal error requires a cause")
	}
	return &InternalError{cause: cause}
}

// Error implements error.
func (e *InternalError) Error() string {
	return fmt.Sprintf("internal MCP client failure: %v", e.cause)
}

// Unwrap returns the implementation failure.
func (e *InternalError) Unwrap() error {
	return e.cause
}

// NewToolExecutionError preserves the validated result returned by a tool that
// set MCP's isError flag.
func NewToolExecutionError(response CallResponse) *ToolExecutionError {
	response.Content = response.Content.Clone()
	response.StructuredContent = append([]byte(nil), response.StructuredContent...)
	return &ToolExecutionError{Response: response}
}

// Error implements error.
func (e *ToolExecutionError) Error() string {
	if len(e.Response.Content) == 0 {
		return "MCP tool execution error"
	}
	var messages []string
	for _, block := range e.Response.Content {
		if text, ok := block.(*toolcontent.TextContent); ok {
			messages = append(messages, text.Text)
		}
	}
	if len(messages) == 0 {
		return "MCP tool execution error"
	}
	return "MCP tool execution error: " + strings.Join(messages, "\n")
}

// unknownToolOutcome preserves explicit protocol errors and marks other failures
// after a tool request was sent, so callers cannot mistake them for a rejection.
func unknownToolOutcome(err error) error {
	var protocol *Error
	if err == nil || errors.As(err, &protocol) {
		return err
	}
	return NewOutcomeUnknownError(err)
}
