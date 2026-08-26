// Package mcp defines errors that callers can inspect without parsing text.
package mcp

import (
	"fmt"
	"strings"
)

type (
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
	response.Content = append([]string(nil), response.Content...)
	response.StructuredContent = append([]byte(nil), response.StructuredContent...)
	return &ToolExecutionError{Response: response}
}

// Error implements error.
func (e *ToolExecutionError) Error() string {
	if len(e.Response.Content) == 0 {
		return "MCP tool execution error"
	}
	return "MCP tool execution error: " + strings.Join(e.Response.Content, "\n")
}
