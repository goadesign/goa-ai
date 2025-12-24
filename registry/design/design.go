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

	// gRPC transport configuration
	GRPC(func() {
		Response("not_found", CodeNotFound)
		Response("validation_error", CodeInvalidArgument)
		Response("service_unavailable", CodeUnavailable)
	})
})

var _ = Service("registry", func() {
	Description("Internal tool registry gateway for toolset discovery and tool invocation")

	// Set a non-generic protobuf package to avoid collisions when multiple Goa
	// services named "registry" are linked into the same binary.
	GRPC(func() {
		Package("goa_ai_registry")
	})

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
		Description("Initiate a tool call by publishing a tool call message to the toolset request stream and returning the tool use identifier and result stream identifier.")
		Payload(CallToolPayload)
		Result(CallToolResult)
		Error("not_found")
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})
})

// ---- Payload and Result Types ----

var SemVer = Type("SemVer", String, func() {
	Description("Semantic version string (for example, \"1.0.0\" or \"v1.0.0\").")
	Pattern(`^v?\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?$`)
	Example("1.0.0")
})

var ToolCallMeta = Type("ToolCallMeta", func() {
	Description("Context metadata propagated alongside tool calls for routing, correlation, and domain injection (for example, session-scoped data access).")
	Field(1, "run_id", String, "Run identifier for the agent execution that issued this tool call.", func() {
		MinLength(1)
		Example("run_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(2, "session_id", String, "Chat session identifier used to scope tool behavior and persistence.", func() {
		MinLength(1)
		Example("sess_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(3, "turn_id", String, "Turn identifier within the session.", func() {
		MinLength(1)
		Example("turn_0001")
	})
	Field(4, "tool_call_id", String, "Tool call identifier used for correlation with model provider tool calls.", func() {
		MinLength(1)
		Example("call_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(5, "parent_tool_call_id", String, "Parent tool call identifier when the tool call is nested.", func() {
		MinLength(1)
		Example("call_01J3K9Q9T6E2G7N0G2ZQH2KX19Z")
	})
	Required("run_id", "session_id")
})

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
	Field(3, "version", SemVer, "Semantic version of the toolset.")
	Field(4, "tags", ArrayOf(String), "Tags for categorization and filtering", func() {
		Example([]string{"data", "etl", "analytics"})
	})
	Field(5, "tools", ArrayOf(ToolSchema), "Tool definitions with their schemas")
	Required("name", "tools")
})

var RegisterResult = Type("RegisterResult", func() {
	Description("Result of a successful toolset registration")
	Field(1, "stream_id", String, "Pulse stream identifier for receiving tool requests", func() {
		MinLength(1)
		Example("toolset:data-tools:requests")
	})
	Field(2, "registered_at", String, "ISO 8601 timestamp of registration", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Required("stream_id", "registered_at")
})

var UnregisterPayload = Type("UnregisterPayload", func() {
	Description("Payload for unregistering a toolset")
	Field(1, "name", String, "Name of the toolset to unregister", func() {
		MinLength(1)
		Example("data-tools")
	})
	Required("name")
})

var EmitToolResultPayload = Type("EmitToolResultPayload", func() {
	Description("Payload for emitting a tool execution result")
	Field(1, "tool_use_id", String, "Unique identifier for the tool invocation", func() {
		MinLength(1)
		MaxLength(256)
		Example("call-abc123")
	})
	Field(2, "result", Any, "Tool execution result (JSON-serializable)")
	Field(3, "error", ToolError, "Error details if execution failed")
	Required("tool_use_id")
})

var PongPayload = Type("PongPayload", func() {
	Description("Payload for responding to a health check ping")
	Field(1, "ping_id", String, "ID of the ping being acknowledged", func() {
		MinLength(1)
		MaxLength(256)
		Example("ping-xyz789")
	})
	Field(2, "toolset", String, "Name of the toolset responding", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Required("ping_id", "toolset")
})

var ListToolsetsPayload = Type("ListToolsetsPayload", func() {
	Description("Payload for listing toolsets with optional filtering")
	Field(1, "tags", ArrayOf(String), "Filter by tags (all must match)", func() {
		Example([]string{"data", "etl"})
	})
})

var ListToolsetsResult = Type("ListToolsetsResult", func() {
	Description("Result containing list of toolsets")
	Field(1, "toolsets", ArrayOf(ToolsetInfo), "List of registered toolsets")
	Required("toolsets")
})

var GetToolsetPayload = Type("GetToolsetPayload", func() {
	Description("Payload for retrieving a specific toolset")
	Field(1, "name", String, "Name of the toolset to retrieve", func() {
		MinLength(1)
		Example("data-tools")
	})
	Required("name")
})

var SearchPayload = Type("SearchPayload", func() {
	Description("Payload for searching toolsets")
	Field(1, "query", String, "Search query string", func() {
		MinLength(1)
		MaxLength(1024)
		Example("data processing")
	})
	Required("query")
})

var SearchResult = Type("SearchResult", func() {
	Description("Result containing search matches")
	Field(1, "toolsets", ArrayOf(ToolsetInfo), "Matching toolsets")
	Required("toolsets")
})

var CallToolPayload = Type("CallToolPayload", func() {
	Description("Payload for initiating a tool call through the registry gateway.")
	Field(1, "toolset", String, "Toolset registration identifier used for routing (for example, \"atlas_data.atlas.read\").", func() {
		MinLength(1)
		MaxLength(256)
		Example("atlas_data.atlas.read")
	})
	Field(2, "tool", String, "Globally unique tool identifier of the form \"toolset.tool\" (for example, \"atlas.read.get_time_series\").", func() {
		MinLength(1)
		MaxLength(256)
		Example("atlas.read.get_time_series")
	})
	Field(3, "payload_json", Bytes, "Canonical JSON payload for the tool call. Must validate against the registered payload schema.", func() {
		MinLength(1)
		Example([]byte(`{"query":"compressor_1 key events"}`))
	})
	Field(4, "meta", ToolCallMeta, "Execution metadata propagated alongside the tool call.")
	Required("toolset", "tool", "payload_json", "meta")
})

var CallToolResult = Type("CallToolResult", func() {
	Description("Result of initiating a tool call through the registry gateway.")
	Field(1, "tool_use_id", String, "Unique identifier for this invocation", func() {
		MinLength(1)
		MaxLength(256)
		Example("call-abc123")
	})
	Field(2, "result_stream_id", String, "Pulse stream identifier that will receive the tool result message.", func() {
		MinLength(1)
		MaxLength(256)
		Example("result:call-abc123")
	})
	Required("tool_use_id", "result_stream_id")
})

// ---- Shared Types ----

var Toolset = Type("Toolset", func() {
	Description("Complete toolset definition with all tool schemas")
	Field(1, "name", String, "Unique name for the toolset", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Field(2, "description", String, "Human-readable description", func() {
		Example("Tools for data processing and analysis")
	})
	Field(3, "version", SemVer, "Semantic version of the toolset.")
	Field(4, "tags", ArrayOf(String), "Tags for categorization", func() {
		Example([]string{"data", "etl"})
	})
	Field(5, "tools", ArrayOf(ToolSchema), "Tool schemas included in the toolset.")
	Field(6, "stream_id", String, "Pulse stream identifier", func() {
		MinLength(1)
		Example("toolset:data-tools:requests")
	})
	Field(7, "registered_at", String, "ISO 8601 registration timestamp", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Required("name", "tools", "stream_id", "registered_at")
})

var ToolsetInfo = Type("ToolsetInfo", func() {
	Description("Toolset metadata for listing and search results")
	Field(1, "name", String, "Unique name for the toolset", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Field(2, "description", String, "Human-readable description", func() {
		Example("Tools for data processing and analysis")
	})
	Field(3, "version", SemVer, "Semantic version of the toolset.")
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

var Tool = Type("Tool", func() {
	Description("DEPRECATED: Tool definitions are represented via ToolSchema in this API.")
	Field(1, "name", String, "Tool identifier.", func() {
		MinLength(1)
		MaxLength(256)
		Example("analyze")
	})
	Field(2, "description", String, "Human-readable description.", func() {
		Example("Analyze data and return insights")
	})
	Field(3, "input_schema", Bytes, "JSON Schema for tool input parameters.", func() {
		MinLength(1)
		Example([]byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`))
	})
	Field(4, "output_schema", Bytes, "JSON Schema for tool output (optional).", func() {
		Example([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`))
	})
	Required("name", "input_schema")
})

var ToolSchema = Type("ToolSchema", func() {
	Description("Tool schema declaration for registration with the tool registry gateway.")
	Field(1, "name", String, "Globally unique tool identifier of the form \"toolset.tool\".", func() {
		MinLength(1)
		MaxLength(256)
		Example("atlas.read.get_time_series")
	})
	Field(2, "description", String, "Human-readable description of what the tool does.", func() {
		Example("Fetch a time series for a point over a time window.")
	})
	Field(3, "tags", ArrayOf(String), "Optional tags used for policy, routing, or UI filtering.", func() {
		Example([]string{"atlas", "data", "read"})
	})
	Field(4, "payload_schema", Bytes, "Canonical JSON schema for the tool payload.", func() {
		MinLength(1)
		Example([]byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`))
	})
	Field(5, "result_schema", Bytes, "Canonical JSON schema for the tool result.", func() {
		MinLength(1)
		Example([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`))
	})
	Field(6, "sidecar_schema", Bytes, "Canonical JSON schema for the tool sidecar (UI-only), when present.", func() {
		Example([]byte(`{"type":"object","properties":{"artifact_kind":{"type":"string"}}}`))
	})
	Required("name", "payload_schema", "result_schema")
})

var ToolError = Type("ToolError", func() {
	Description("Error details from tool execution")
	Field(1, "code", String, "Error code", func() {
		MinLength(1)
		MaxLength(128)
		Example("execution_failed")
	})
	Field(2, "message", String, "Error message", func() {
		MinLength(1)
		MaxLength(4096)
		Example("Failed to connect to database")
	})
	Required("code", "message")
})
