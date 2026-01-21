// Package toolregistry defines the canonical wire protocol and stream naming
// helpers used by the tool registry gateway and tool providers/consumers.
package toolregistry

import (
	"encoding/json"
	"errors"
	"strings"

	goa "goa.design/goa/v3/pkg"

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

		// TraceParent and TraceState carry W3C Trace Context headers for distributed
		// tracing across Pulse boundaries. These fields are optional and may be empty.
		// When set, consumers should extract them into their context before starting
		// spans for handling the tool call.
		TraceParent string `json:"traceparent,omitempty"`
		TraceState  string `json:"tracestate,omitempty"`

		// Baggage carries the W3C baggage header when the global propagator includes
		// baggage propagation (common for OTEL setups). Optional.
		Baggage string `json:"baggage,omitempty"`
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
		// Issues optionally carries structured field-level validation issues.
		// When present, consumers can build a RetryHint without parsing Message.
		Issues []*tools.FieldIssue `json:"issues,omitempty"`
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

// NewToolResultErrorMessageWithIssues constructs an error tool result message that
// includes structured validation issues for building retry hints.
func NewToolResultErrorMessageWithIssues(toolUseID, code, message string, issues []*tools.FieldIssue) ToolResultMessage {
	out := NewToolResultErrorMessage(toolUseID, code, message)
	if out.Error == nil {
		return out
	}
	if len(issues) == 0 {
		return out
	}
	out.Error.Issues = cloneFieldIssues(issues)
	return out
}

// ValidationIssues extracts structured field-level validation issues from err.
//
// It supports two common sources:
//   - Generated tool-codec validation errors that expose Issues() []*tools.FieldIssue
//   - Goa ServiceErrors (possibly merged) that use Goa validation error names
//     (missing_field, invalid_length, etc.) and populate ServiceError.Field.
//
// ValidationIssues returns nil when err does not represent a field-level validation failure.
func ValidationIssues(err error) []*tools.FieldIssue {
	if err == nil {
		return nil
	}

	var ip interface {
		Issues() []*tools.FieldIssue
	}
	if errors.As(err, &ip) {
		return cloneFieldIssues(ip.Issues())
	}

	var se *goa.ServiceError
	if !errors.As(err, &se) {
		return nil
	}

	hist := se.History()
	if len(hist) == 0 {
		return nil
	}

	issues := make([]*tools.FieldIssue, 0, len(hist))
	for _, h := range hist {
		if h == nil {
			continue
		}
		if !isGoaValidationConstraint(h.Name) {
			continue
		}
		if h.Field == nil || *h.Field == "" {
			continue
		}
		field := *h.Field
		field = strings.TrimPrefix(field, "body.")
		if field == "" {
			continue
		}
		issues = append(issues, &tools.FieldIssue{
			Field:      field,
			Constraint: h.Name,
		})
	}
	if len(issues) == 0 {
		return nil
	}
	return issues
}

func cloneFieldIssues(in []*tools.FieldIssue) []*tools.FieldIssue {
	if len(in) == 0 {
		return nil
	}
	out := make([]*tools.FieldIssue, 0, len(in))
	for _, is := range in {
		if is == nil {
			continue
		}
		cp := *is
		if len(cp.Allowed) > 0 {
			cp.Allowed = append([]string(nil), cp.Allowed...)
		}
		out = append(out, &cp)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isGoaValidationConstraint(name string) bool {
	switch name {
	case goa.InvalidFieldType,
		goa.MissingField,
		goa.InvalidEnumValue,
		goa.InvalidFormat,
		goa.InvalidPattern,
		goa.InvalidRange,
		goa.InvalidLength:
		return true
	default:
		return false
	}
}
