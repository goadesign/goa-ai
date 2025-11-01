package dsl

import (
	apitypes "goa.design/goa-ai/apitypes"
	. "goa.design/goa/v3/dsl"
)

var AgentToolTelemetry = Type("AgentToolTelemetry", func() {
	Description("Telemetry metadata gathered during tool execution.")
	Attribute("duration_ms", Int64, "Wall-clock duration in milliseconds", func() {
		Minimum(0)
		Example(1250)
	})
	Attribute("tokens_used", Int, "Total tokens consumed by the tool call", func() {
		Minimum(0)
		Example(512)
	})
	Attribute("model", String, "Identifier of the model used by the tool", func() {
		Example("claude-3-opus")
	})
	Attribute("extra", MapOf(String, Any), "Tool-specific telemetry key/value pairs", func() {
		Example(map[string]any{"trace_id": "abc-123"})
	})
	CreateFrom(apitypes.ToolTelemetry{})
	ConvertTo(apitypes.ToolTelemetry{})
})

var AgentRetryHint = Type("AgentRetryHint", func() {
	Description("Structured guidance emitted after a tool failure.")
	Attribute("reason", String, "Categorized reason for the retry guidance", func() {
		Enum(
			"invalid_arguments",
			"missing_fields",
			"malformed_response",
			"timeout",
			"rate_limited",
			"tool_unavailable",
		)
		Example("invalid_arguments")
	})
	Attribute("tool", String, "Qualified tool name associated with the hint", func() {
		MinLength(1)
		Example("assistant.search")
	})
	Attribute("restrict_to_tool", Boolean, "Restrict subsequent planner turns to this tool")
	Attribute("missing_fields", ArrayOf(String), "Missing or invalid fields that caused the failure", func() {
		Example([]any{"query", "filters"})
	})
	Attribute("example_input", MapOf(String, Any), "Representative payload that satisfies validation", func() {
		Example(map[string]any{"query": "latest status"})
	})
	Attribute("prior_input", MapOf(String, Any), "Payload that triggered the failure", func() {
		Example(map[string]any{"query": ""})
	})
	Attribute("clarifying_question", String, "Question that callers should answer to proceed", func() {
		Example("Which incident should I summarize?")
	})
	Attribute("message", String, "Human-readable guidance for logs or UI", func() {
		Example("Tool returned 400: invalid request payload")
	})
	Required("reason", "tool")
	CreateFrom(apitypes.RetryHint{})
	ConvertTo(apitypes.RetryHint{})
})

var AgentToolError = Type("AgentToolError", func() {
	Description("Structured tool error chain captured during execution.")
	Attribute("message", String, "Human-readable error summary", func() {
		Example("timeout contacting upstream search API")
	})
	Attribute("cause", "AgentToolError", "Nested cause describing the underlying failure")
	CreateFrom(apitypes.ToolError{})
	ConvertTo(apitypes.ToolError{})
})

var AgentToolEvent = Type("AgentToolEvent", func() {
	Description("Outcome of an executed tool call.")
	Attribute("name", String, "Tool identifier", func() {
		MinLength(1)
		Example("assistant.search")
	})
	Attribute("result", Any, "Tool result content when the call succeeds", func() {
		Example(map[string]any{"documents": []any{"alpha.md", "beta.md"}})
	})
	Attribute("error", AgentToolError, "Structured error returned by the tool")
	Attribute("retry_hint", AgentRetryHint, "Retry guidance emitted by the tool on failure")
	Attribute("telemetry", AgentToolTelemetry, "Telemetry metadata captured during execution")
	Required("name")
	Example("search-failure", func() {
		Description("Search tool failed with retry guidance.")
		Value(Val{
			"name": "assistant.search",
			"error": Val{
				"message": "invalid filter value",
			},
			"retry_hint": Val{
				"reason":  "invalid_arguments",
				"tool":    "assistant.search",
				"message": "Ensure the filter is a recognized status value.",
			},
		})
	})
	CreateFrom(apitypes.ToolResult{})
	ConvertTo(apitypes.ToolResult{})
})

var AgentPlannerAnnotation = Type("AgentPlannerAnnotation", func() {
	Description("Planner observation persisted alongside run history.")
	Attribute("text", String, "Annotation emitted by the planner", func() {
		MinLength(1)
		Example("Calling search to gather the latest incidents.")
	})
	Attribute("labels", MapOf(String, String), "Structured metadata associated with the note", func() {
		Example(map[string]string{"type": "reasoning"})
	})
	CreateFrom(apitypes.PlannerAnnotation{})
	ConvertTo(apitypes.PlannerAnnotation{})
})

var AgentMessage = Type("AgentMessage", func() {
	Description("Single conversational message in the agent transcript.")
	Attribute("role", String, "Role that produced the message", func() {
		Enum("user", "assistant", "tool", "system")
		Example("user")
	})
	Attribute("content", String, "Message content", func() {
		MinLength(1)
		Example("Summarize today's incidents.")
	})
	Attribute("meta", MapOf(String, Any), "Optional structured metadata attached to the message", func() {
		Example(map[string]any{"message_id": "sys-1"})
	})
	Required("role", "content")
	CreateFrom(apitypes.AgentMessage{})
	ConvertTo(apitypes.AgentMessage{})
})

var AgentRunPayload = Type("AgentRunPayload", func() {
	Description("Payload submitted to agent Run endpoints.")
	Attribute("agent_id", String, "Agent identifier to invoke (optional when bound to a single agent)", func() {
		Example("orchestrator.chat")
	})
	Attribute("run_id", String, "Caller-provided run identifier", func() {
		MinLength(1)
		Example("run-abc")
	})
	Attribute("session_id", String, "Session identifier used for grouping runs", func() {
		MinLength(1)
		Example("session-123")
	})
	Attribute("turn_id", String, "Turn identifier associated with the run", func() {
		Example("turn-7")
	})
	Attribute("messages", ArrayOf(AgentMessage), "Complete conversation history supplied to the agent", func() {
		MinLength(1)
		Example([]any{
			Val{
				"role":    "system",
				"content": "You are a concise assistant.",
				"meta":    Val{"message_id": "sys-1"},
			},
			Val{
				"role":    "user",
				"content": "Summarize the latest status update.",
				"meta":    Val{"message_id": "user-1"},
			},
		})
	})
	Attribute("labels", MapOf(String, String), "Caller-supplied labels forwarded to the runtime", func() {
		Example(map[string]string{"tenant": "acme"})
	})
	Attribute("metadata", MapOf(String, Any), "Arbitrary metadata forwarded to the runtime", func() {
		Example(map[string]any{"priority": "p1"})
	})
	Required("messages")
	CreateFrom(apitypes.RunInput{})
	ConvertTo(apitypes.RunInput{})
})

var AgentRunResult = Type("AgentRunResult", func() {
	Description("Terminal output produced by agent Run endpoints.")
	Attribute("agent_id", String, "Identifier of the agent that produced the result", func() {
		MinLength(1)
		Example("orchestrator.chat")
	})
	Attribute("run_id", String, "Identifier of the completed run", func() {
		MinLength(1)
		Example("run-abc")
	})
	Attribute("final", AgentMessage, "Final assistant response returned to the caller", func() {
		Example(Val{
			"role":    "assistant",
			"content": "All systems nominal. No outstanding action items.",
		})
	})
	Attribute("tool_events", ArrayOf(AgentToolEvent), "Tool events emitted during the final turn")
	Attribute("notes", ArrayOf(AgentPlannerAnnotation), "Planner annotations captured during completion")
	Required("agent_id", "run_id", "final")
	CreateFrom(apitypes.RunOutput{})
	ConvertTo(apitypes.RunOutput{})
})

var AgentRunChunk = Type("AgentRunChunk", func() {
	Description("Streaming chunk emitted while an agent run progresses.")
	Attribute("type", String, "Kind of chunk being delivered", func() {
		Enum("message", "tool_call", "tool_result", "status")
		Example("message")
	})
	Attribute("message", String, "Assistant message fragment.", func() {
		Example("Processing request...")
	})
	Attribute("tool_call", AgentToolCallChunk, "Tool call scheduling notification.")
	Attribute("tool_result", AgentToolResultChunk, "Tool result payload notification.")
	Attribute("status", AgentRunStatusChunk, "Run status update.")
	Required("type")
})

var AgentToolCallChunk = Type("AgentToolCallChunk", func() {
	Description("Chunk describing a scheduled tool call.")
	Attribute("id", String, "Tool call identifier", func() {
		MinLength(1)
		Example("call-001")
	})
	Attribute("name", String, "Tool name", func() {
		MinLength(1)
		Example("assistant.search")
	})
	Attribute("payload", Any, "Payload submitted to the tool")
	Required("id", "name")
})

var AgentToolResultChunk = Type("AgentToolResultChunk", func() {
	Description("Chunk containing the result of a tool call.")
	Attribute("id", String, "Tool call identifier", func() {
		MinLength(1)
		Example("call-001")
	})
	Attribute("result", Any, "Decoded tool result payload")
	Attribute("error", AgentToolError, "Tool error, when the call failed")
	Required("id")
})

var AgentRunStatusChunk = Type("AgentRunStatusChunk", func() {
	Description("Run status change emitted during streaming.")
	Attribute("state", String, "Current run state", func() {
		Enum("started", "paused", "resumed", "completed")
		Example("started")
	})
	Attribute("message", String, "Optional status annotation", func() {
		Example("Awaiting human approval")
	})
	Required("state")
})
