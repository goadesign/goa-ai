// Package a2a provides A2A (Agent-to-Agent) client implementations
// for invoking skills via HTTP SSE or JSON-RPC transports. Callers adapt
// transport-specific clients to the unified Caller interface used by agent toolset
// adapters.
package a2a

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

// Caller invokes A2A skills on behalf of the runtime-generated adapters. It is
// implemented by transport-specific clients (HTTP streaming, etc.).
type Caller interface {
	SendTask(ctx context.Context, req SendTaskRequest) (SendTaskResponse, error)
}

// Error represents a JSON-RPC error returned by the A2A server.
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

// SendTaskRequest describes the skill invocation issued by the runtime.
type SendTaskRequest struct {
	// Suite identifies the A2A agent (server name) associated with the skill.
	Suite string
	// Skill is the A2A-local skill identifier (without the suite prefix).
	Skill string
	// Payload is the JSON-encoded skill arguments produced by the runtime.
	Payload json.RawMessage
}

// SendTaskResponse captures the A2A task result returned by the caller.
type SendTaskResponse struct {
	// Result is the JSON payload returned by the A2A server.
	Result json.RawMessage
	// Structured carries optional structured content blobs emitted by A2A skills.
	Structured json.RawMessage
}
