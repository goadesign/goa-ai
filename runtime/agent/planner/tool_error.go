package planner

import toolerrors "goa.design/goa-ai/runtime/agent/toolerrors"

// RetryReason categorizes the type of failure that triggered a retry hint.
// Policy engines use this to make informed decisions about retry strategies
// (e.g., disable tools, adjust caps, request human intervention).
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

	// RetryReasonTimeout indicates the tool execution exceeded time limits.
	// Policy engines may reduce caps or disable the tool for this run.
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
