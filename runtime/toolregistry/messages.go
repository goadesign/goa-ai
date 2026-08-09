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
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	goa "goa.design/goa/v3/pkg"
)

type (
	// ToolCallMessageType is the type discriminator for toolset stream messages.
	ToolCallMessageType string

	// ToolRetryReason identifies one registry-owned reason for republishing an
	// admitted call without exposing a terminal failure to the planner.
	ToolRetryReason string

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
		ToolUseID             string
		RegistrationToken     string
		ExecutionDeadline     time.Time
		ResultStreamExpiresAt time.Time
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
		// ExecutionDeadlineUnixMilli is the Redis-selected absolute deadline
		// that bounds provider dispatch and executor waiting.
		ExecutionDeadlineUnixMilli int64 `json:"execution_deadline_unix_ms,omitempty"`
		// ResultStreamExpiresAtUnixMilli is the registry-selected absolute
		// expiration shared by the call record and every result-stream handle.
		ResultStreamExpiresAtUnixMilli int64           `json:"result_stream_expires_at_unix_ms,omitempty"`
		PingID                         string          `json:"ping_id,omitempty"`
		Tool                           tools.Ident     `json:"tool,omitempty"`
		Payload                        json.RawMessage `json:"payload,omitempty"`
		Meta                           *ToolCallMeta   `json:"meta,omitempty"`

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

	// ToolResultMessage is published to a per-call result stream. The gateway
	// interprets only the retry-control variant; consumers decode terminal
	// success and error variants using compiled tool contracts.
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
		// Retry asks registry orchestration to republish this exact admitted call.
		// It is mutually exclusive with every terminal success and error field.
		Retry *ToolRetry `json:"retry,omitempty"`
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

	// ToolError is one terminal canonical planner failure published by providers.
	ToolError struct {
		// Code is the stable machine-readable failure classification.
		Code string `json:"code"`
		// Failure is the canonical planner classification and recovery contract.
		Failure *planner.ToolFailure `json:"failure"`
	}

	// ToolRetry is a non-terminal registry orchestration instruction. It never
	// carries planner failure semantics.
	ToolRetry struct {
		// Reason identifies the retryable transport condition.
		Reason ToolRetryReason `json:"reason"`
		// RetryAfterMillis is the bounded delay before exact republication.
		RetryAfterMillis int64 `json:"retry_after_ms,omitempty"`
	}
)

const (
	// MessageTypeCall indicates a tool invocation message on a toolset stream.
	MessageTypeCall ToolCallMessageType = "call"
	// MessageTypePing indicates a health ping message on a toolset stream.
	MessageTypePing ToolCallMessageType = "ping"

	// ResultEventKey is the Pulse event name used to publish canonical terminal
	// results and retry-control envelopes to a per-call result stream.
	ResultEventKey = "result"
	// ProviderConsumerGroup is the one canonical Pulse consumer group shared by
	// every provider replica for a toolset stream.
	ProviderConsumerGroup = "provider"

	// OutputDeltaEventKey is the Pulse event name used to publish best-effort tool
	// output delta messages to a per-call result stream.
	OutputDeltaEventKey = "output_delta"

	// ProviderOverloadRetryAfter is the provider-requested delay before the
	// registry republishes an admitted call after queue saturation.
	ProviderOverloadRetryAfter = 250 * time.Millisecond
	// MaxProviderOverloadRetryAfter bounds overload delay at the message boundary.
	MaxProviderOverloadRetryAfter = 5 * time.Second
	// MaxToolCallMetaIDLength matches the Goa boundary for every identifier
	// propagated in ToolCallMeta.
	MaxToolCallMetaIDLength = 256

	// ToolRetryReasonProviderOverloaded means provider intake was saturated
	// before execution began.
	ToolRetryReasonProviderOverloaded ToolRetryReason = "provider_overloaded"

	// ToolErrorCodeStaleRegistration completes a queued call whose admission
	// generation no longer matches the admitted provider.
	ToolErrorCodeStaleRegistration = "stale_registration"
	// ToolErrorCodeOutcomeUnknown completes a claimed call when the exact
	// provider lease disappears before terminal publication. The provider may
	// already have performed the side effect, so execution must not transfer.
	ToolErrorCodeOutcomeUnknown = "outcome_unknown"
)

// DecodeServerData decodes the canonical server-only item envelope carried by
// planner tool results. Kind-specific payloads remain raw JSON for decoding by
// the generated codec declared on the corresponding tool specification.
func DecodeServerData(data []byte) ([]*ServerDataItem, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var items []*ServerDataItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode server data: %w", err)
	}
	return items, nil
}

// EncodeServerData encodes registry wire items for validation by the generated
// canonicalizer attached to the receiving tool specification.
func EncodeServerData(items []*ServerDataItem) (json.RawMessage, error) {
	if len(items) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode server data: %w", err)
	}
	return data, nil
}

// NewToolCallMessage constructs a tool invocation message.
func NewToolCallMessage(
	registrationToken, toolUseID string,
	executionDeadline time.Time,
	resultStreamExpiresAt time.Time,
	tool tools.Ident,
	payload json.RawMessage,
	meta *ToolCallMeta,
) ToolCallMessage {
	return ToolCallMessage{
		RegistrationToken:              registrationToken,
		Type:                           MessageTypeCall,
		ToolUseID:                      toolUseID,
		ExecutionDeadlineUnixMilli:     executionDeadline.UnixMilli(),
		ResultStreamExpiresAtUnixMilli: resultStreamExpiresAt.UnixMilli(),
		Tool:                           tool,
		Payload:                        payload,
		Meta:                           meta,
	}
}

// ValidateToolCallMessage enforces the structural Pulse request boundary
// without deciding whether a call's absolute expiration has elapsed. The
// registry owns that decision with Redis time.
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
		if message.ExecutionDeadlineUnixMilli <= 0 {
			return fmt.Errorf("execution deadline must be a positive Unix millisecond timestamp")
		}
		if message.ResultStreamExpiresAtUnixMilli <= 0 {
			return fmt.Errorf("result stream expiration must be a positive Unix millisecond timestamp")
		}
		if message.ExecutionDeadlineUnixMilli >= message.ResultStreamExpiresAtUnixMilli {
			return fmt.Errorf("execution deadline must precede result stream expiration")
		}
		if message.Meta == nil ||
			message.Meta.RunID == "" ||
			message.Meta.SessionID == "" ||
			message.Meta.ToolCallID == "" {
			return fmt.Errorf("tool call run, session, and tool call metadata are required")
		}
		for name, value := range map[string]string{
			"run ID":              message.Meta.RunID,
			"session ID":          message.Meta.SessionID,
			"turn ID":             message.Meta.TurnID,
			"tool call ID":        message.Meta.ToolCallID,
			"parent tool call ID": message.Meta.ParentToolCallID,
		} {
			if len(value) > MaxToolCallMetaIDLength {
				return fmt.Errorf("%s exceeds %d bytes", name, MaxToolCallMetaIDLength)
			}
			if strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("%s must not contain NUL", name)
			}
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

// NewToolResultRetryMessage constructs a non-terminal retry-control message.
func NewToolResultRetryMessage(
	registrationToken, toolUseID string,
	reason ToolRetryReason,
	retryAfter time.Duration,
) ToolResultMessage {
	return ToolResultMessage{
		RegistrationToken: registrationToken,
		ToolUseID:         toolUseID,
		Retry: &ToolRetry{
			Reason:           reason,
			RetryAfterMillis: retryAfter.Milliseconds(),
		},
	}
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
			Failure: defaultToolFailure(code, message),
		},
	}
}

// NewToolResultServiceErrorMessage constructs a terminal provider result while
// preserving a service-owned ToolFailureProvider contract when present.
func NewToolResultServiceErrorMessage(
	registrationToken, toolUseID string,
	tool tools.Ident,
	code string,
	err error,
) ToolResultMessage {
	out := NewToolResultErrorMessage(registrationToken, toolUseID, code, err.Error())
	issues := ValidationIssues(err)
	var provider planner.ToolFailureProvider
	if errors.As(err, &provider) {
		out.Error.Failure = planner.CloneToolFailure(provider.ToolFailure(tool))
		if out.Error.Failure.Recovery.Action == planner.RecoveryCorrectCall &&
			len(out.Error.Failure.Recovery.Issues) == 0 {
			out.Error.Failure.Recovery.Issues = tools.CloneFieldIssues(issues)
		}
	} else if len(issues) > 0 {
		out.Error.Failure = defaultToolFailure("invalid_arguments", err.Error())
		out.Error.Failure.Recovery.Issues = tools.CloneFieldIssues(issues)
	}
	return out
}

// NewToolResultInvalidArgumentsMessage constructs a correct-call failure with
// structured validation issues. Other recovery actions cannot carry issues.
func NewToolResultInvalidArgumentsMessage(
	registrationToken, toolUseID, message string,
	issues []*tools.FieldIssue,
) ToolResultMessage {
	out := NewToolResultErrorMessage(registrationToken, toolUseID, "invalid_arguments", message)
	out.Error.Failure.Recovery.Issues = tools.CloneFieldIssues(issues)
	return out
}

// ValidateToolResultMessage enforces the top-level success, terminal-error, or
// retry-control union before a consumer acts on the message.
func ValidateToolResultMessage(message ToolResultMessage) error {
	if err := ValidateRegistrationToken(message.RegistrationToken); err != nil {
		return err
	}
	if err := ValidateToolUseID(message.ToolUseID); err != nil {
		return err
	}
	if message.Retry != nil {
		if message.Error != nil {
			return fmt.Errorf("retry and error are both set")
		}
		if rawMessageHasNonNullJSON(message.Result) {
			return fmt.Errorf("retry and result are both set")
		}
		if message.Bounds != nil {
			return fmt.Errorf("retry and bounds are both set")
		}
		if len(message.ServerData) > 0 {
			return fmt.Errorf("retry and server data are both set")
		}
		if message.Retry.Reason != ToolRetryReasonProviderOverloaded {
			return fmt.Errorf("unsupported tool retry reason %q", message.Retry.Reason)
		}
		if message.Retry.RetryAfterMillis <= 0 ||
			message.Retry.RetryAfterMillis > MaxProviderOverloadRetryAfter.Milliseconds() {
			return fmt.Errorf(
				"tool retry delay %dms is outside (0,%dms]",
				message.Retry.RetryAfterMillis,
				MaxProviderOverloadRetryAfter.Milliseconds(),
			)
		}
		return nil
	}
	if message.Error == nil {
		return nil
	}
	if message.Error.Failure == nil {
		return fmt.Errorf("error is missing failure contract")
	}
	if message.Error.Code == "" {
		return fmt.Errorf("error code is required")
	}
	if rawMessageHasNonNullJSON(message.Result) {
		return fmt.Errorf("error and result are both set")
	}
	if message.Bounds != nil {
		return fmt.Errorf("error and bounds are both set")
	}
	if len(message.ServerData) > 0 {
		return fmt.Errorf("error and server data are both set")
	}
	if err := planner.ValidateToolFailure(message.Error.Failure); err != nil {
		return fmt.Errorf("failure contract is invalid: %w", err)
	}
	return nil
}

// defaultToolFailure maps provider protocol failures into the canonical
// planner contract when the owning service did not supply a richer contract.
func defaultToolFailure(code, message string) *planner.ToolFailure {
	failure := &planner.ToolFailure{
		Kind:  planner.FailureInternal,
		Error: planner.NewToolError(message),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryFinish,
		},
	}
	switch code {
	case ToolErrorCodeStaleRegistration, "service_unavailable":
		failure.Kind = planner.FailureUnavailable
		failure.Recovery.Action = planner.RecoveryReplan
	case ToolErrorCodeOutcomeUnknown:
		failure.Kind = planner.FailureInternal
		failure.Recovery.Action = planner.RecoveryFinish
	case "rate_limited":
		failure.Kind = planner.FailureRateLimited
		failure.Recovery.Action = planner.RecoveryReplan
	case "invalid_input":
		failure.Kind = planner.FailureDomainRejection
		failure.Recovery.Action = planner.RecoveryReplan
	case "invalid_arguments":
		failure.Kind = planner.FailureInvalidCall
		failure.Recovery.Action = planner.RecoveryCorrectCall
	case "timeout":
		failure.Kind = planner.FailureTimeout
	}
	return failure
}

// rawMessageHasNonNullJSON reports whether raw carries a real JSON value.
func rawMessageHasNonNullJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
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
