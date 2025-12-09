// Package design defines the internal tool registry service using Goa DSL.
// The registry acts as both a catalog and gateway — agents discover toolsets
// through the registry and invoke tools through it.
package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = API("registry", func() {
	Title("Internal Tool Registry API")
	Description("Gateway service for toolset discovery and tool invocation via Pulse streams")
	Version("1.0")
	Server("registry", func() {
		Host("dev", func() {
			URI("grpc://localhost:9090")
		})
		Services("registry")
	})

	// Error definitions
	Error("not_found", ErrorResult, "Toolset or tool not found")
	Error("validation_error", ErrorResult, "Payload validation failed")
	Error("service_unavailable", ErrorResult, "No healthy providers available")
	Error("timeout", ErrorResult, "Tool invocation timed out")

	// gRPC transport configuration
	GRPC(func() {
		Response("not_found", CodeNotFound)
		Response("validation_error", CodeInvalidArgument)
		Response("service_unavailable", CodeUnavailable)
		Response("timeout", CodeDeadlineExceeded)
	})
})

var _ = Service("registry", func() {
	Description("Internal tool registry gateway for toolset discovery and tool invocation")

	// ---- Provider Operations ----

	Method("Register", func() {
		Description("Register a toolset with the registry")
		Payload(RegisterPayload)
		Result(RegisterResult)
		GRPC(func() {})
	})

	Method("Unregister", func() {
		Description("Unregister a toolset from the registry")
		Payload(UnregisterPayload)
		Error("not_found")
		GRPC(func() {})
	})

	Method("EmitToolResult", func() {
		Description("Emit a tool execution result to the result stream")
		Payload(EmitToolResultPayload)
		GRPC(func() {})
	})

	Method("Pong", func() {
		Description("Respond to a health check ping")
		Payload(PongPayload)
		GRPC(func() {})
	})

	// ---- Discovery Operations ----

	Method("ListToolsets", func() {
		Description("List all registered toolsets with optional tag filtering")
		Payload(ListToolsetsPayload)
		Result(ListToolsetsResult)
		GRPC(func() {})
	})

	Method("GetToolset", func() {
		Description("Get a specific toolset by name including all tool schemas")
		Payload(GetToolsetPayload)
		Result(Toolset)
		Error("not_found")
		GRPC(func() {})
	})

	Method("Search", func() {
		Description("Search toolsets by keyword matching name, description, or tags")
		Payload(SearchPayload)
		Result(SearchResult)
		GRPC(func() {})
	})

	// ---- Invocation Operations ----

	Method("CallTool", func() {
		Description("Invoke a tool through the registry gateway")
		Payload(CallToolPayload)
		Result(CallToolResult)
		Error("not_found")
		Error("validation_error")
		Error("service_unavailable")
		Error("timeout")
		GRPC(func() {})
	})
})

// ---- Payload and Result Types ----

// RegisterPayload is the request payload for registering a toolset.
var RegisterPayload = Type("RegisterPayload", func() {
	Description("Payload for registering a toolset with the registry")
	Field(1, "name", String, "Unique name for the toolset", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Field(2, "description", String, "Human-readable description of the toolset", func() {
		MaxLength(4096)
		Example("Tools for data processing and analysis")
	})
	Field(3, "version", String, "Semantic version of the toolset", func() {
		Pattern(`^v?\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?$`)
		Example("1.0.0")
	})
	Field(4, "tags", ArrayOf(String), "Tags for categorization and filtering", func() {
		Example([]string{"data", "etl", "analytics"})
	})
	Field(5, "tools", ArrayOf(ToolSchema), "Tool definitions with their schemas")
	Required("name", "tools")
})

// RegisterResult is the response for a successful registration.
var RegisterResult = Type("RegisterResult", func() {
	Description("Result of a successful toolset registration")
	Field(1, "stream_id", String, "Pulse stream identifier for receiving tool requests", func() {
		Example("toolset:data-tools:requests")
	})
	Field(2, "registered_at", String, "ISO 8601 timestamp of registration", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Required("stream_id", "registered_at")
})

// UnregisterPayload is the request payload for unregistering a toolset.
var UnregisterPayload = Type("UnregisterPayload", func() {
	Description("Payload for unregistering a toolset")
	Field(1, "name", String, "Name of the toolset to unregister", func() {
		MinLength(1)
		Example("data-tools")
	})
	Required("name")
})

// EmitToolResultPayload is the request payload for emitting a tool result.
var EmitToolResultPayload = Type("EmitToolResultPayload", func() {
	Description("Payload for emitting a tool execution result")
	Field(1, "tool_use_id", String, "Unique identifier for the tool invocation", func() {
		MinLength(1)
		Example("call-abc123")
	})
	Field(2, "result", Any, "Tool execution result (JSON-serializable)")
	Field(3, "error", ToolError, "Error details if execution failed")
	Required("tool_use_id")
})

// PongPayload is the request payload for responding to a health check ping.
var PongPayload = Type("PongPayload", func() {
	Description("Payload for responding to a health check ping")
	Field(1, "ping_id", String, "ID of the ping being acknowledged", func() {
		MinLength(1)
		Example("ping-xyz789")
	})
	Field(2, "toolset", String, "Name of the toolset responding", func() {
		MinLength(1)
		Example("data-tools")
	})
	Required("ping_id", "toolset")
})

// ListToolsetsPayload is the request payload for listing toolsets.
var ListToolsetsPayload = Type("ListToolsetsPayload", func() {
	Description("Payload for listing toolsets with optional filtering")
	Field(1, "tags", ArrayOf(String), "Filter by tags (all must match)", func() {
		Example([]string{"data", "etl"})
	})
})

// ListToolsetsResult is the response for listing toolsets.
var ListToolsetsResult = Type("ListToolsetsResult", func() {
	Description("Result containing list of toolsets")
	Field(1, "toolsets", ArrayOf(ToolsetInfo), "List of registered toolsets")
	Required("toolsets")
})

// GetToolsetPayload is the request payload for getting a specific toolset.
var GetToolsetPayload = Type("GetToolsetPayload", func() {
	Description("Payload for retrieving a specific toolset")
	Field(1, "name", String, "Name of the toolset to retrieve", func() {
		MinLength(1)
		Example("data-tools")
	})
	Required("name")
})

// SearchPayload is the request payload for searching toolsets.
var SearchPayload = Type("SearchPayload", func() {
	Description("Payload for searching toolsets")
	Field(1, "query", String, "Search query string", func() {
		MinLength(1)
		MaxLength(1024)
		Example("data processing")
	})
	Required("query")
})

// SearchResult is the response for searching toolsets.
var SearchResult = Type("SearchResult", func() {
	Description("Result containing search matches")
	Field(1, "toolsets", ArrayOf(ToolsetInfo), "Matching toolsets")
	Required("toolsets")
})

// CallToolPayload is the request payload for invoking a tool.
var CallToolPayload = Type("CallToolPayload", func() {
	Description("Payload for invoking a tool through the registry")
	Field(1, "toolset", String, "Name of the toolset containing the tool", func() {
		MinLength(1)
		Example("data-tools")
	})
	Field(2, "tool", String, "Name of the tool to invoke", func() {
		MinLength(1)
		Example("analyze")
	})
	Field(3, "payload", Any, "Tool input payload (must match tool's input schema)")
	Required("toolset", "tool")
})

// CallToolResult is the response for a tool invocation.
var CallToolResult = Type("CallToolResult", func() {
	Description("Result of a tool invocation")
	Field(1, "tool_use_id", String, "Unique identifier for this invocation", func() {
		Example("call-abc123")
	})
	Field(2, "result", Any, "Tool execution result")
	Field(3, "error", ToolError, "Error details if execution failed")
	Required("tool_use_id")
})

// ---- Shared Types ----

// Toolset is the complete toolset definition including all tool schemas.
var Toolset = Type("Toolset", func() {
	Description("Complete toolset definition with all tool schemas")
	Field(1, "name", String, "Unique name for the toolset", func() {
		Example("data-tools")
	})
	Field(2, "description", String, "Human-readable description", func() {
		Example("Tools for data processing and analysis")
	})
	Field(3, "version", String, "Semantic version", func() {
		Example("1.0.0")
	})
	Field(4, "tags", ArrayOf(String), "Tags for categorization", func() {
		Example([]string{"data", "etl"})
	})
	Field(5, "tools", ArrayOf(Tool), "Tool definitions")
	Field(6, "stream_id", String, "Pulse stream identifier", func() {
		Example("toolset:data-tools:requests")
	})
	Field(7, "registered_at", String, "ISO 8601 registration timestamp", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Required("name", "tools", "stream_id", "registered_at")
})

// ToolsetInfo is the metadata for a toolset (without full tool schemas).
var ToolsetInfo = Type("ToolsetInfo", func() {
	Description("Toolset metadata for listing and search results")
	Field(1, "name", String, "Unique name for the toolset", func() {
		Example("data-tools")
	})
	Field(2, "description", String, "Human-readable description", func() {
		Example("Tools for data processing and analysis")
	})
	Field(3, "version", String, "Semantic version", func() {
		Example("1.0.0")
	})
	Field(4, "tags", ArrayOf(String), "Tags for categorization", func() {
		Example([]string{"data", "etl"})
	})
	Field(5, "tool_count", Int, "Number of tools in the toolset", func() {
		Minimum(0)
		Example(5)
	})
	Field(6, "registered_at", String, "ISO 8601 registration timestamp", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Required("name", "tool_count", "registered_at")
})

// Tool is a single tool definition within a toolset.
var Tool = Type("Tool", func() {
	Description("Tool definition with input and output schemas")
	Field(1, "name", String, "Tool identifier", func() {
		MinLength(1)
		Example("analyze")
	})
	Field(2, "description", String, "Human-readable description", func() {
		Example("Analyze data and return insights")
	})
	Field(3, "input_schema", Bytes, "JSON Schema for tool input parameters")
	Field(4, "output_schema", Bytes, "JSON Schema for tool output (optional)")
	Required("name", "input_schema")
})

// ToolSchema is used in registration payloads.
var ToolSchema = Type("ToolSchema", func() {
	Description("Tool schema for registration")
	Field(1, "name", String, "Tool identifier", func() {
		MinLength(1)
		Example("analyze")
	})
	Field(2, "description", String, "Human-readable description", func() {
		Example("Analyze data and return insights")
	})
	Field(3, "input_schema", Bytes, "JSON Schema for tool input parameters")
	Field(4, "output_schema", Bytes, "JSON Schema for tool output (optional)")
	Required("name", "input_schema")
})

// ToolError represents an error from tool execution.
var ToolError = Type("ToolError", func() {
	Description("Error details from tool execution")
	Field(1, "code", String, "Error code", func() {
		Example("execution_failed")
	})
	Field(2, "message", String, "Error message", func() {
		Example("Failed to connect to database")
	})
	Required("code", "message")
})

// ---- Stream Message Types ----
// These types are used for Pulse stream communication between the registry
// and providers. They are defined here for documentation and potential
// code generation but are primarily used in the runtime implementation.

// ToolCallMessage is published to toolset streams for tool invocations.
var ToolCallMessage = Type("ToolCallMessage", func() {
	Description("Message published to toolset streams for tool invocations or health checks")
	Field(1, "type", String, "Message type: 'call' or 'ping'", func() {
		Enum("call", "ping")
		Example("call")
	})
	Field(2, "tool_use_id", String, "Unique identifier for tool invocations", func() {
		Example("call-abc123")
	})
	Field(3, "ping_id", String, "Unique identifier for health check pings", func() {
		Example("ping-xyz789")
	})
	Field(4, "tool", String, "Name of the tool to invoke", func() {
		Example("analyze")
	})
	Field(5, "payload", Any, "Tool input payload")
	Required("type")
})

// ToolResultMessage is published to result streams.
var ToolResultMessage = Type("ToolResultMessage", func() {
	Description("Message published to result streams for tool execution results")
	Field(1, "tool_use_id", String, "Unique identifier for the tool invocation", func() {
		Example("call-abc123")
	})
	Field(2, "result", Any, "Tool execution result")
	Field(3, "error", ToolError, "Error details if execution failed")
	Required("tool_use_id")
})
