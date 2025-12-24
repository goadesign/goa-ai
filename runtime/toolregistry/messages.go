// Package toolregistry defines the canonical wire protocol and stream naming
// helpers used by the tool registry gateway and tool providers/consumers.
package toolregistry

import (
	"encoding/json"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ToolCallMessageType is the type discriminator for toolset stream messages.
	ToolCallMessageType string

	// ToolCallMeta is execution metadata propagated alongside tool calls.
	// Providers may use this metadata to scope data access and persistence (for example,
	// applying session-scoped policies without polluting tool payload schemas).
	ToolCallMeta struct {
		RunID            string `json:"run_id"`
		SessionID        string `json:"session_id"`
		TurnID           string `json:"turn_id,omitempty"`
		ToolCallID       string `json:"tool_call_id,omitempty"`
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	}

	// ToolCallMessage is published to a toolset request stream for tool invocations
	// and provider health checks.
	ToolCallMessage struct {
		Type      ToolCallMessageType `json:"type"`
		ToolUseID string              `json:"tool_use_id,omitempty"`
		PingID    string              `json:"ping_id,omitempty"`
		Tool      tools.Ident         `json:"tool,omitempty"`
		Payload   json.RawMessage     `json:"payload,omitempty"`
		Meta      *ToolCallMeta       `json:"meta,omitempty"`
	}

	// ToolResultMessage is published to a per-call result stream. The gateway never
	// interprets these bytes; consumers decode them using compiled tool codecs.
	ToolResultMessage struct {
		ToolUseID string          `json:"tool_use_id"`
		Result    json.RawMessage `json:"result_json,omitempty"`
		Artifacts []Artifact      `json:"artifacts,omitempty"`
		Error     *ToolError      `json:"error,omitempty"`
	}

	// Artifact is a tool-produced artifact payload published alongside results.
	// Artifacts are never sent to model providers; they are UI/policy-facing data.
	Artifact struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}

	// ToolError is a structured tool error published by providers.
	ToolError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
)

const (
	// MessageTypeCall indicates a tool invocation message on a toolset stream.
	MessageTypeCall ToolCallMessageType = "call"
	// MessageTypePing indicates a health ping message on a toolset stream.
	MessageTypePing ToolCallMessageType = "ping"
)

// NewToolCallMessage constructs a tool invocation message.
func NewToolCallMessage(toolUseID string, tool tools.Ident, payload json.RawMessage, meta *ToolCallMeta) ToolCallMessage {
	return ToolCallMessage{
		Type:      MessageTypeCall,
		ToolUseID: toolUseID,
		Tool:      tool,
		Payload:   payload,
		Meta:      meta,
	}
}

// NewPingMessage constructs a health ping message.
func NewPingMessage(pingID string) ToolCallMessage {
	return ToolCallMessage{
		Type:   MessageTypePing,
		PingID: pingID,
	}
}

// NewToolResultMessage constructs a successful tool result message.
func NewToolResultMessage(toolUseID string, result json.RawMessage, artifacts []Artifact) ToolResultMessage {
	return ToolResultMessage{
		ToolUseID: toolUseID,
		Result:    result,
		Artifacts: artifacts,
	}
}

// NewToolResultErrorMessage constructs an error tool result message.
func NewToolResultErrorMessage(toolUseID, code, message string) ToolResultMessage {
	return ToolResultMessage{
		ToolUseID: toolUseID,
		Error: &ToolError{
			Code:    code,
			Message: message,
		},
	}
}


