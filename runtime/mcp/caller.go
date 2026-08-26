// Package mcp provides MCP clients that invoke tools through stdio or HTTP.
// Each client implements the Caller interface used by generated agent toolset
// adapters.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
)

type (
	// ClientInfo identifies the application connecting to an MCP server.
	ClientInfo struct {
		// Name is the application name sent during MCP initialization.
		Name string
		// Version is the application version sent during MCP initialization.
		Version string
	}

	// Caller invokes MCP tools on behalf of the runtime-generated adapters. It is
	// implemented by transport-specific clients.
	Caller interface {
		CallTool(ctx context.Context, req CallRequest) (CallResponse, error)
	}

	// Error represents a JSON-RPC error returned by the MCP server.
	Error struct {
		// Code is the JSON-RPC error code returned by the server.
		Code int
		// Message explains why the server rejected the request.
		Message string
	}

	// CallRequest describes the toolset/tool invocation issued by the runtime.
	CallRequest struct {
		// Tool is the MCP-local tool identifier (without the suite prefix).
		Tool string
		// Payload is the JSON-encoded tool arguments produced by the runtime.
		Payload json.RawMessage
	}

	// CallResponse captures the MCP tool result returned by the caller.
	CallResponse struct {
		// Content contains every text block in the order returned by the MCP server.
		Content []string
		// StructuredContent is the optional JSON object returned by the MCP server.
		StructuredContent json.RawMessage
	}
)

const (
	// JSONRPCParseError means the server could not parse the JSON request.
	JSONRPCParseError = -32700
	// JSONRPCInvalidRequest means the decoded value is not a JSON-RPC request.
	JSONRPCInvalidRequest = -32600
	// JSONRPCMethodNotFound means the requested method does not exist.
	JSONRPCMethodNotFound = -32601
	// JSONRPCInvalidParams means the method arguments do not satisfy its contract.
	JSONRPCInvalidParams = -32602
	// JSONRPCInternalError means the server failed while handling the request.
	JSONRPCInternalError = -32603
)

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Validate reports whether the caller can send this identity in an MCP
// initialize request.
func (i ClientInfo) Validate() error {
	if i.Name == "" {
		return errors.New("mcp: client name is required")
	}
	if i.Version == "" {
		return errors.New("mcp: client version is required")
	}
	return nil
}
