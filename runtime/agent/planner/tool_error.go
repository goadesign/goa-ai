package planner

import toolerrors "goa.design/goa-ai/runtime/agent/toolerrors"

// RetryReason categorizes the failure described by a RetryHint. Policy engines
// use this to make informed handling decisions such as retrying, disabling
// tools, adjusting caps, or requesting human intervention.
type RetryReason string

// ToolError represents a structured tool failure and is an alias to the runtime
// toolerrors type. Planners use this to return structured tool errors to the runtime.
type ToolError = toolerrors.ToolError

const (
	// RetryReasonInvalidArguments indicates the tool call failed due to invalid
	// or malformed input arguments (schema violation, type mismatch, etc.).
	RetryReasonInvalidArguments RetryReason = "invalid_arguments"

	// RetryReasonMissingFields indicates required fields were missing or empty
	// in the tool call payload. The planner may populate MissingFields to specify
	// which fields are needed.
	RetryReasonMissingFields RetryReason = "missing_fields"

	// RetryReasonMalformedResponse indicates the tool returned data that couldn't
	// be parsed or didn't match the expected schema (e.g., invalid JSON).
	RetryReasonMalformedResponse RetryReason = "malformed_response"

	// RetryReasonTimeout classifies a tool failure caused by exceeding a time or
	// budget limit. It is a terminal classification, not a retry instruction:
	// hints carrying this reason never set RestrictToTool, so the tool is not
	// re-issued. Consumers use it to page timeouts distinctly from internal
	// failures rather than to drive recovery.
	RetryReasonTimeout RetryReason = "timeout"

	// RetryReasonRateLimited indicates the tool or underlying service is rate-limited.
	// Policy engines may back off or disable the tool temporarily.
	RetryReasonRateLimited RetryReason = "rate_limited"

	// RetryReasonToolUnavailable indicates the tool is temporarily or permanently
	// unavailable (service down, not configured, etc.).
	RetryReasonToolUnavailable RetryReason = "tool_unavailable"
)

// NewToolError constructs a ToolError with the provided message.
func NewToolError(message string) *ToolError {
	return toolerrors.New(message)
}

// NewToolErrorWithCause wraps an existing error with a ToolError message.
func NewToolErrorWithCause(message string, cause error) *ToolError {
	return toolerrors.NewWithCause(message, cause)
}

// ToolErrorFromError converts an arbitrary error into a ToolError chain.
func ToolErrorFromError(err error) *ToolError {
	return toolerrors.FromError(err)
}

// ToolErrorf formats according to a format specifier and returns the string as
// a ToolError.
func ToolErrorf(format string, args ...any) *ToolError {
	return toolerrors.Errorf(format, args...)
}

// AllowsRetry reports whether the hint authorizes another tool attempt in the
// current run. Timeout hints classify a terminal failure for policy and UX
// consumers; every other defined reason describes a recoverable failure.
func (h *RetryHint) AllowsRetry() bool {
	if h == nil {
		return false
	}
	switch h.Reason {
	case RetryReasonInvalidArguments,
		RetryReasonMissingFields,
		RetryReasonMalformedResponse,
		RetryReasonRateLimited,
		RetryReasonToolUnavailable:
		return true
	case RetryReasonTimeout:
		return false
	default:
		panic("planner: unknown retry reason " + h.Reason)
	}
}
