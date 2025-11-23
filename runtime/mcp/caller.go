// Package mcp provides MCP (Model Context Protocol) client implementations
// for invoking tools via stdio, HTTP SSE, or JSON-RPC transports. Callers adapt
// transport-specific clients to the unified Caller interface used by agent toolset
// adapters.
package mcp

import (
	"context"
	"encoding/json"
)

const (
	// JSON-RPC canonical error codes per spec.
	JSONRPCParseError     = -32700
	JSONRPCInvalidRequest = -32600
	JSONRPCMethodNotFound = -32601
	JSONRPCInvalidParams  = -32602
	JSONRPCInternalError  = -32603
)

// Caller invokes MCP tools on behalf of the runtime-generated adapters. It is
// implemented by transport-specific clients (stdio, HTTP streaming, etc.).
type Caller interface {
	CallTool(ctx context.Context, req CallRequest) (CallResponse, error)
}

// Error represents a JSON-RPC error returned by the MCP server.
type Error struct {
	Code    int
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// CallRequest describes the toolset/tool invocation issued by the runtime.
type CallRequest struct {
	// Suite identifies the MCP toolset (server name) associated with the tool.
	Suite string
	// Tool is the MCP-local tool identifier (without the suite prefix).
	Tool string
	// Payload is the JSON-encoded tool arguments produced by the runtime.
	Payload json.RawMessage
}

// CallResponse captures the MCP tool result returned by the caller.
type CallResponse struct {
	// Result is the JSON payload returned by the MCP server.
	Result json.RawMessage
	// Structured carries optional structured content blobs emitted by MCP tools.
	Structured json.RawMessage
}
