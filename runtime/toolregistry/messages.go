// Package toolregistry defines the canonical wire protocol and stream naming
// helpers used by the tool registry gateway and tool providers/consumers.
package toolregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent"
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
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
		TurnID    string `json:"turn_id,omitempty"`
		// ToolCallID is the model/provider call identity. The registry preserves it
		// as metadata and derives the separate global ToolUseID from RunID plus
		// ToolCallID.
		ToolCallID       string `json:"tool_call_id"`
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	}

	// ToolCallRef identifies one registry-routed invocation and the exact
	// admission generation whose events the caller may accept.
	ToolCallRef struct {
		ToolUseID         string
		RegistrationToken string
		ResultStreamTTL   time.Duration
	}

	// ToolCallMessage is published to a toolset request stream for tool invocations
	// and provider health checks.
	ToolCallMessage struct {
		// RegistrationToken is the exact admission generation used to validate
		// and route this message. Providers reject calls from any other admission.
		RegistrationToken string `json:"registration_token"`
		// ToolUseID is the globally unique transport identity for this request.
		Type      ToolCallMessageType `json:"type"`
		ToolUseID string              `json:"tool_use_id,omitempty"`
		// ResultStreamTTLMillis is the registry-selected sliding retention used
		// by every producer and consumer of this call's result stream.
		ResultStreamTTLMillis int64           `json:"result_stream_ttl_ms,omitempty"`
		PingID                string          `json:"ping_id,omitempty"`
		Tool                  tools.Ident     `json:"tool,omitempty"`
		Payload               json.RawMessage `json:"payload,omitempty"`
		Meta                  *ToolCallMeta   `json:"meta,omitempty"`

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
		// RegistrationToken echoes the exact token stamped on the tool call.
		RegistrationToken string          `json:"registration_token"`
		ToolUseID         string          `json:"tool_use_id"`
		Result            json.RawMessage `json:"result_json,omitempty"`
		// Bounds carries canonical bounded-result metadata projected out-of-band
		// from the semantic result payload when the tool declares a bounded-result
		// contract.
		Bounds *agent.Bounds `json:"bounds,omitempty"`
		// ServerData carries server-only metadata about the tool execution that must
		// not be serialized into model provider requests.
		//
		// This is the canonical home for any non-model payloads emitted alongside a
		// tool result. Consumers may project it into different observer views (for
		// example, UI render cards vs persistence-only evidence), but the wire
		// protocol keeps a single server-side envelope.
		ServerData []*ServerDataItem `json:"server_data,omitempty"`
		Error      *ToolError        `json:"error,omitempty"`
	}

	// ToolOutputDeltaMessage is published to a per-call result stream while a tool
	// is still running. It streams partial output to consumers for improved UX
	// (live output panels) without changing the final ToolResultMessage.
	//
	// Contract:
	//   - This is best-effort and may be dropped by consumers.
	//   - Deltas are not persisted by default; the canonical output remains the
	//     final tool result payload.
	ToolOutputDeltaMessage struct {
		// RegistrationToken echoes the exact token stamped on the tool call so
		// reused transport IDs cannot forward output from an older admission.
		RegistrationToken string `json:"registration_token"`
		ToolUseID         string `json:"tool_use_id"`
		// Stream identifies which logical output channel produced the delta
		// (for example, "stdout", "stderr", "log", "progress").
		Stream string `json:"stream"`
		Delta  string `json:"delta"`
	}

	// ServerDataItem is server-only tool output published alongside the canonical
	// tool result JSON. Server data is never sent to model providers.
	ServerDataItem struct {
		Kind     string          `json:"kind"`
		Audience string          `json:"audience"`
		Data     json.RawMessage `json:"data"`
	}

	// ToolError is a structured tool error published by providers.
	ToolError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		// RetryAfterMillis is a bounded registry orchestration delay supplied by
		// transient provider-overload results.
		RetryAfterMillis int64 `json:"retry_after_ms,omitempty"`
		// Issues optionally carries structured field-level validation issues.
		// Consumers preserve these in a correct-call recovery directive without
		// parsing Message.
		Issues []*tools.FieldIssue `json:"issues,omitempty"`
	}
)

const (
	// MessageTypeCall indicates a tool invocation message on a toolset stream.
	MessageTypeCall ToolCallMessageType = "call"
	// MessageTypePing indicates a health ping message on a toolset stream.
	MessageTypePing ToolCallMessageType = "ping"

	// ResultEventKey is the Pulse event name used to publish canonical tool result
	// envelopes to a per-call result stream.
	ResultEventKey = "result"

	// OutputDeltaEventKey is the Pulse event name used to publish best-effort tool
	// output delta messages to a per-call result stream.
	OutputDeltaEventKey = "output_delta"

	// ProviderOverloadRetryAfter is the provider-requested delay before the
	// registry republishes an admitted call after queue saturation.
	ProviderOverloadRetryAfter = 250 * time.Millisecond
	// MaxProviderOverloadRetryAfter bounds overload delay at the message boundary.
	MaxProviderOverloadRetryAfter = 5 * time.Second

	// ToolErrorCodeStaleRegistration completes a queued call whose admission
	// generation no longer matches the admitted provider.
	ToolErrorCodeStaleRegistration = "stale_registration"
	// ToolErrorCodeProviderOverloaded completes a call that exceeded the
	// provider's exact waiting bound.
	ToolErrorCodeProviderOverloaded = "provider_overloaded"
)

// NewToolCallMessage constructs a tool invocation message.
func NewToolCallMessage(
	registrationToken, toolUseID string,
	resultStreamTTL time.Duration,
	tool tools.Ident,
	payload json.RawMessage,
	meta *ToolCallMeta,
) ToolCallMessage {
	return ToolCallMessage{
		RegistrationToken:     registrationToken,
		Type:                  MessageTypeCall,
		ToolUseID:             toolUseID,
		ResultStreamTTLMillis: resultStreamTTL.Milliseconds(),
		Tool:                  tool,
		Payload:               payload,
		Meta:                  meta,
	}
}

// ValidateToolCallMessage enforces the Pulse request boundary before
// publication and again before provider dispatch.
func ValidateToolCallMessage(message ToolCallMessage) error {
	if err := ValidateRegistrationToken(message.RegistrationToken); err != nil {
		return err
	}
	switch message.Type {
	case MessageTypePing:
		if message.PingID == "" {
			return fmt.Errorf("ping ID is required")
		}
		return nil
	case MessageTypeCall:
		if err := ValidateToolUseID(message.ToolUseID); err != nil {
			return err
		}
		if err := ValidateResultStreamTTLMillis(message.ResultStreamTTLMillis); err != nil {
			return err
		}
		if message.Meta == nil ||
			message.Meta.RunID == "" ||
			message.Meta.SessionID == "" ||
			message.Meta.ToolCallID == "" {
			return fmt.Errorf("tool call run, session, and tool call metadata are required")
		}
		if len(message.Meta.ToolCallID) > MaxToolUseIDLength {
			return fmt.Errorf("tool call ID exceeds %d bytes", MaxToolUseIDLength)
		}
		return nil
	default:
		return fmt.Errorf("unsupported tool call message type %q", message.Type)
	}
}

// NewPingMessage constructs a health ping message.
func NewPingMessage(registrationToken, pingID string) ToolCallMessage {
	return ToolCallMessage{
		RegistrationToken: registrationToken,
		Type:              MessageTypePing,
		PingID:            pingID,
	}
}

// NewToolResultMessage constructs a successful tool result message.
func NewToolResultMessage(registrationToken, toolUseID string, result json.RawMessage) ToolResultMessage {
	return ToolResultMessage{
		RegistrationToken: registrationToken,
		ToolUseID:         toolUseID,
		Result:            result,
	}
}

// NewToolResultMessageWithServerData constructs a successful tool result message with
// additional server-only metadata.
func NewToolResultMessageWithServerData(
	registrationToken, toolUseID string,
	result json.RawMessage,
	serverData []*ServerDataItem,
) ToolResultMessage {
	out := NewToolResultMessage(registrationToken, toolUseID, result)
	out.ServerData = serverData
	return out
}

// NewToolOutputDeltaMessage constructs a tool output delta message.
func NewToolOutputDeltaMessage(registrationToken, toolUseID, stream, delta string) ToolOutputDeltaMessage {
	return ToolOutputDeltaMessage{
		RegistrationToken: registrationToken,
		ToolUseID:         toolUseID,
		Stream:            stream,
		Delta:             delta,
	}
}

// NewToolResultErrorMessage constructs an error tool result message.
func NewToolResultErrorMessage(registrationToken, toolUseID, code, message string) ToolResultMessage {
	return ToolResultMessage{
		RegistrationToken: registrationToken,
		ToolUseID:         toolUseID,
		Error: &ToolError{
			Code:    code,
			Message: message,
		},
	}
}

// NewToolResultErrorMessageWithIssues constructs an error tool result message that
// includes structured validation issues for building correction directives.
func NewToolResultErrorMessageWithIssues(
	registrationToken, toolUseID, code, message string,
	issues []*tools.FieldIssue,
) ToolResultMessage {
	out := NewToolResultErrorMessage(registrationToken, toolUseID, code, message)
	if out.Error == nil {
		return out
	}
	if len(issues) == 0 {
		return out
	}
	out.Error.Issues = tools.CloneFieldIssues(issues)
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
		return tools.CloneFieldIssues(ip.Issues())
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
