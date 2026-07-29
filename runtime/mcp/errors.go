// Package mcp exposes typed protocol-boundary errors so agent adapters can
// classify MCP failures without parsing transport messages.
package mcp

import (
	"encoding/json"
	"fmt"
)

type (
	// MalformedResponseError reports an MCP response that violated the expected
	// response envelope or tool-result contract.
	MalformedResponseError struct {
		cause error
	}

	// InternalError reports an MCP client invariant or implementation failure.
	InternalError struct {
		cause error
	}

	// ToolExecutionError reports an MCP tools/call response whose isError flag
	// says the remote tool rejected or failed the call.
	ToolExecutionError struct {
		result json.RawMessage
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

// NewInternalError wraps an MCP client invariant or implementation failure.
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

// NewToolExecutionError preserves the exact valid JSON result returned with an
// MCP tool execution error.
func NewToolExecutionError(result json.RawMessage) *ToolExecutionError {
	if len(result) == 0 || !json.Valid(result) {
		panic("mcp: tool execution error requires a valid JSON result")
	}
	return &ToolExecutionError{result: append(json.RawMessage(nil), result...)}
}

// Error implements error.
func (e *ToolExecutionError) Error() string {
	return fmt.Sprintf("MCP tool execution error: %s", e.result)
}
