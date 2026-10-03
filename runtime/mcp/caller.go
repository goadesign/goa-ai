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
		// Name is the application name sent with each MCP request.
		Name string `json:"name"`
		// Version is the application version sent with each MCP request.
		Version string `json:"version"`
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
		// Data preserves the server's encoded error details, including JSON null.
		Data json.RawMessage
	}

	// CallRequest describes the toolset/tool invocation issued by the runtime.
	CallRequest struct {
		// Tool is the MCP-local tool identifier (without the suite prefix).
		Tool string
		// Payload is the JSON-encoded tool arguments produced by the runtime.
		Payload json.RawMessage
		// Continuation is absent for the first round and retained for later rounds.
		Continuation *CallContinuation
	}

	// CallResponse captures the MCP tool result returned by the caller.
	CallResponse struct {
		// Content contains every typed block in the order returned by the MCP server.
		Content []ContentBlock
		// StructuredContent is a present JSON value; zero bytes means absent, while null is present.
		StructuredContent json.RawMessage
		// InputRequired describes an unfinished call instead of a successful result.
		InputRequired *InputRequired
	}

	// InputRequired carries server-owned inputs and state for continuing one MCP operation.
	// The host supplies answers; the agent model never selects protocol IDs or state.
	InputRequired struct {
		// Requests maps server input IDs to their request method and parameters.
		Requests map[string]InputRequest
		// RequestState is echoed unchanged on the next round of this operation.
		RequestState *string
	}

	// InputRequest describes a host interaction requested by an unfinished MCP call.
	InputRequest struct {
		// Method is the protocol interaction, such as elicitation/create.
		Method string `json:"method"`
		// Params retains the exact interaction contract supplied by the server.
		Params json.RawMessage `json:"params"`
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
	// HeaderMismatch means HTTP headers do not match the JSON-RPC request.
	HeaderMismatch = -32020
	// MissingRequiredClientCapability means the host cannot fulfill a required interaction.
	MissingRequiredClientCapability = -32021
	// UnsupportedProtocolVersion means the server does not implement the requested revision.
	UnsupportedProtocolVersion = -32022
)

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Validate reports whether the caller can send this identity in each MCP request.
func (i ClientInfo) Validate() error {
	if i.Name == "" {
		return errors.New("mcp: client name is required")
	}
	if i.Version == "" {
		return errors.New("mcp: client version is required")
	}
	return nil
}
