// Package design defines the internal tool registry service using Goa DSL.
// The registry acts as both a catalog and gateway — agents discover toolsets
// through the registry and invoke tools through it.
package design

import (
	. "goa.design/goa/v3/dsl"

	"goa.design/goa-ai/runtime/toolregistry"
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
	Error("service_unavailable", ErrorResult, "Registry routing infrastructure or healthy providers are unavailable")
	Error("admission_blocked", ErrorResult, "Another admission still has active provider leases")
	Error("admission_retired", ErrorResult, "The requested admission was intentionally retired")
	Error("admission_conflict", ErrorResult, "The expected admission token does not match the catalog record")

	// gRPC transport configuration
	GRPC(func() {
		Response("not_found", CodeNotFound)
		Response("validation_error", CodeInvalidArgument)
		Response("service_unavailable", CodeUnavailable)
		Response("admission_blocked", CodeUnavailable)
		Response("admission_retired", CodeFailedPrecondition)
		Response("admission_conflict", CodeFailedPrecondition)
	})
})

var _ = Service("registry", func() {
	Description("The registry owns serialized toolset admission generations, provider leases and health, discovery, and routed invocation over Pulse streams. Providers renew leases for the one active schema and admission revision; consumers discover and invoke only healthy admitted providers.")

	// Set a non-generic protobuf package to avoid collisions when multiple Goa
	// services named "registry" are linked into the same binary.
	GRPC(func() {
		Package("goa_ai_registry")
	})

	// ---- Provider Operations ----

	Method("Register", func() {
		Description("Atomically admit or renew one provider-incarnation lease in the catalog admission record. The same schema and admission revision add or renew replicas under one token. A different token replaces the admission after Redis-time pruning proves every old lease expired and atomically tombstones the prior token; otherwise admission_blocked asks the provider to retry. Any candidate in the permanent retired-token set returns admission_retired and cannot resurrect.")
		Payload(RegisterPayload)
		Result(RegisterResult)
		Error("admission_blocked")
		Error("admission_retired")
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("ReleaseProvider", func() {
		Description("Release one exact provider-incarnation lease from the admission token after that Serve lifecycle has stopped claiming work and settled in-flight calls. Missing incarnations and stale tokens succeed without mutation; infrastructure failures are retryable.")
		Payload(ReleaseProviderPayload)
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("Unregister", func() {
		Description("Intentionally retire the exact active admission while preserving its provider leases until graceful release or expiry and atomically adding its token to the permanent retired-token set. Repeating the same-token retirement succeeds; a stale token returns admission_conflict. Retirement removes the toolset from discovery and routing and permanently prevents that exact token from registering again.")
		Payload(UnregisterPayload)
		Error("admission_conflict")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("Pong", func() {
		Description("Atomically record shared consumer-group liveness for a token-and-membership-epoch health ping. The responding provider incarnation must hold an unexpired lease in that same catalog record.")
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
		Description("Admit or attach to one run-scoped tool call. The registry atomically owns publication by tool_use_id and registration token, retries provider overload with bounded backoff, preserves immutable sliding-TTL result history, and returns the exact identity, admission token, and retention used by independent replay readers.")
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
		MaxLength(256)
		Example("run_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(2, "session_id", String, "Chat session identifier used to scope tool behavior and persistence.", func() {
		MinLength(1)
		MaxLength(256)
		Example("sess_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(3, "turn_id", String, "Turn identifier within the session.", func() {
		MinLength(1)
		MaxLength(256)
		Example("turn_0001")
	})
	Field(4, "tool_call_id", String, "Tool call identifier used for correlation with model provider tool calls.", func() {
		MinLength(1)
		MaxLength(256)
		Example("call_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(5, "parent_tool_call_id", String, "Parent tool call identifier when the tool call is nested.", func() {
		MinLength(1)
		MaxLength(256)
		Example("call_01J3K9Q9T6E2G7N0G2ZQH2KX19Z")
	})
	Required("run_id", "session_id", "tool_call_id")
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
	Field(6, "provider_id", String, "Stable identity of the provider process registering this toolset.", func() {
		MinLength(1)
		MaxLength(512)
		Example("atlas-data-7cd8949c8f-k2nrp/atlas_data.atlas.discover")
	})
	Field(7, "admission_revision", String, "Deployment-issued revision shared by every replica of one fenced admission. Reuse it for same-contract scaling and rolling updates; change it only to create a new fenced admission.", func() {
		Pattern(toolregistry.AdmissionRevisionPattern)
		Example("2026-07-23.4+441534ae50f6")
	})
	Field(8, "provider_incarnation_id", String, "Runtime-generated UUID identifying one Serve lifecycle. The provider runtime generates it once and reuses it for every renewal.", func() {
		Format(FormatUUID)
		Example("8af45fe9-5c32-4b46-8da5-d350e98b68f3")
	})
	Required("name", "tools", "provider_id", "admission_revision", "provider_incarnation_id")
})

var RegisterResult = Type("RegisterResult", func() {
	Description("Result of a successful toolset registration")
	Field(1, "registered_at", String, "ISO 8601 timestamp of registration", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Field(2, "registration_token", String, "Deterministic admission-generation token derived from the canonical schema fingerprint and deployment-issued admission revision", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Field(3, "lease_duration_ms", Int64, "Duration of the admitted provider lease in milliseconds", func() {
		Minimum(1)
		Maximum(toolregistry.MaxProviderLeaseDuration.Milliseconds())
		Example(120000)
	})
	Required("registered_at", "registration_token", "lease_duration_ms")
})

var UnregisterPayload = Type("UnregisterPayload", func() {
	Description("Generation-fenced payload for unregistering a toolset")
	Field(1, "name", String, "Name of the toolset to unregister", func() {
		MinLength(1)
		Example("data-tools")
	})
	Field(2, "expected_registration_token", String, "Exact admission-generation token returned by Register for the stopped provider rollout", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Required("name", "expected_registration_token")
})

var ReleaseProviderPayload = Type("ReleaseProviderPayload", func() {
	Description("Exact provider lease release payload")
	Field(1, "name", String, "Name of the toolset whose provider is leaving", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Field(2, "provider_id", String, "Stable identity of the provider process releasing its lease", func() {
		MinLength(1)
		MaxLength(512)
		Example("atlas-data-7cd8949c8f-k2nrp/atlas_data.atlas.discover")
	})
	Field(3, "expected_registration_token", String, "Exact admission-generation token returned by Register", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Field(4, "provider_incarnation_id", String, "Runtime-generated UUID of the exact Serve lifecycle releasing its lease.", func() {
		Format(FormatUUID)
		Example("8af45fe9-5c32-4b46-8da5-d350e98b68f3")
	})
	Required("name", "provider_id", "expected_registration_token", "provider_incarnation_id")
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
	Field(3, "provider_id", String, "Stable identity of the provider instance responding to the ping.", func() {
		MinLength(1)
		MaxLength(512)
		Example("atlas-data-7cd8949c8f-k2nrp/atlas_data.atlas.discover")
	})
	Field(4, "provider_incarnation_id", String, "Runtime-generated UUID of the Serve lifecycle responding to the ping.", func() {
		Format(FormatUUID)
		Example("8af45fe9-5c32-4b46-8da5-d350e98b68f3")
	})
	Required("ping_id", "toolset", "provider_id", "provider_incarnation_id")
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
	Description("Routing contract for awaiting one registry-routed call on its bounded sliding-TTL result stream.")
	Field(1, "tool_use_id", String, "Global transport identifier derived from required run_id and tool_call_id.", func() {
		MinLength(1)
		MaxLength(256)
		Example("call-abc123")
	})
	Field(2, "registration_token", String, "Exact admission-generation token stamped on the routed call", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Field(3, "result_stream_ttl_ms", Int64, "Registry-selected sliding retention for the per-call result stream in milliseconds.", func() {
		Minimum(toolregistry.MinResultStreamTTL.Milliseconds())
		Maximum(toolregistry.MaxResultStreamTTL.Milliseconds())
		Example(900000)
	})
	Required("tool_use_id", "registration_token", "result_stream_ttl_ms")
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
	Field(6, "registered_at", String, "ISO 8601 registration timestamp", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Required("name", "tools", "registered_at")
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
