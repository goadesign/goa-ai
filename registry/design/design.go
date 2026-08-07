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
	Error("call_not_admitted", ErrorResult, "The registry proved that no call record was created and no provider could execute the request")
	Error("admission_blocked", ErrorResult, "Another admission still has active provider leases")
	Error("admission_retired", ErrorResult, "The requested admission was intentionally retired")
	Error("admission_conflict", ErrorResult, "The expected admission token does not match the catalog record")

	// gRPC transport configuration
	GRPC(func() {
		Response("not_found", CodeNotFound)
		Response("validation_error", CodeInvalidArgument)
		Response("service_unavailable", CodeUnavailable)
		Response("call_not_admitted", CodeUnavailable)
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
		Description("Reject providers whose required runtime-owned wire protocol version differs from the registry, then atomically admit or renew one provider-incarnation lease in the catalog admission record. The same wire version, schema, and admission revision add or renew replicas under one token. A different token replaces the admission after Redis-time pruning proves every old lease expired and atomically tombstones the prior token; otherwise admission_blocked asks the provider to retry. Any candidate in the permanent retired-token set returns admission_retired and cannot resurrect.")
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

	Method("DrainProvider", func() {
		Description("Atomically mark one exact provider-incarnation lease non-routable before its Serve lifecycle closes the shared request sink. The provider supplies its configured settlement duration; Redis time extends the draining lease through that full lifecycle so already-claimed work can publish terminal results, while new calls route only when another non-draining provider remains.")
		Payload(DrainProviderPayload)
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
		Description("Reject consumers whose required runtime-owned wire protocol version differs from the registry, then admit or attach to one run-scoped tool call. Catalog or provider-health failures proven to occur before call-record creation return call_not_admitted, so callers may safely choose another plan. The registry atomically owns initial publication and terminal completion by tool_use_id: the call record retains the full canonical terminal through its absolute expiration and restores it when bounded result-stream history was trimmed. Before returning, the registry establishes the result stream so the caller can create a reader immediately.")
		Payload(CallToolPayload)
		Result(CallToolResult)
		Error("not_found")
		Error("validation_error")
		Error("service_unavailable")
		Error("call_not_admitted")
		GRPC(func() {})
	})

	Method("RetryTool", func() {
		Description("Republish one previously admitted call after provider overload recorded in the authoritative call record. The runtime supplies the exact original registration token; the registry rejects a changed active admission before publishing and never rebinds claimed execution to a replacement provider. Before returning either a republished or terminal call, the registry establishes the result stream so the caller can create a reader immediately.")
		Payload(RetryToolPayload)
		Result(CallToolResult)
		Error("not_found")
		Error("validation_error")
		Error("service_unavailable")
		Error("admission_conflict")
		GRPC(func() {})
	})

	Method("CompleteToolCall", func() {
		Description("Publish one canonical terminal result for an admitted call. The registry verifies the exact provider incarnation still owns an unexpired lease and claimed request event, then atomically stores the full terminal in the authoritative call record and appends it to bounded result history. If that exact dispatch lease disappears first, registry-owned settlement commits outcome_unknown because the effect may have occurred; execution ownership never transfers.")
		Payload(CompleteToolCallPayload)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("PublishToolOutputDelta", func() {
		Description("Publish one best-effort output fragment for a claimed live call. The registry verifies the exact provider lease and request-event claim, then atomically appends the delta only while the authoritative call record remains nonterminal.")
		Payload(PublishToolOutputDeltaPayload)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("ReportToolCallOverload", func() {
		Description("Report that an exact provider claim could not enter its bounded worker queue. The registry verifies the provider lease and request-event claim, then atomically appends retry control only while the authoritative call record remains nonterminal.")
		Payload(ProviderToolCallClaimPayload)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("ClaimToolCall", func() {
		Description("Atomically settle one queued request before handler dispatch. The registry authenticates the exact provider lease and request event; only an active non-draining lease may gain immutable execution ownership. Existing owners, retained terminal history, and Redis-owned expiration settle without execution, while stale, draining, or retired unclaimed work receives the canonical stale-generation terminal. Only the exact granted provider incarnation and request event may publish deltas or complete the call; ownership never transfers after a crash.")
		Payload(ProviderToolCallClaimPayload)
		Result(ClaimToolCallResult)
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
		MaxLength(toolregistry.MaxToolCallMetaIDLength)
		Pattern(`^[^\x00]+$`)
		Example("run_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(2, "session_id", String, "Chat session identifier used to scope tool behavior and persistence.", func() {
		MinLength(1)
		MaxLength(toolregistry.MaxToolCallMetaIDLength)
		Pattern(`^[^\x00]+$`)
		Example("sess_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(3, "turn_id", String, "Turn identifier within the session.", func() {
		MinLength(1)
		MaxLength(toolregistry.MaxToolCallMetaIDLength)
		Pattern(`^[^\x00]+$`)
		Example("turn_0001")
	})
	Field(4, "tool_call_id", String, "Tool call identifier used for correlation with model provider tool calls.", func() {
		MinLength(1)
		MaxLength(toolregistry.MaxToolCallMetaIDLength)
		Pattern(`^[^\x00]+$`)
		Example("call_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(5, "parent_tool_call_id", String, "Parent tool call identifier when the tool call is nested.", func() {
		MinLength(1)
		MaxLength(toolregistry.MaxToolCallMetaIDLength)
		Pattern(`^[^\x00]+$`)
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
		Pattern(`^[^\x00]+$`)
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
	Field(9, "wire_protocol_version", Int, "Required runtime-owned version of the provider message envelope. The registry admits only its exact canonical version.", func() {
		Enum(toolregistry.WireProtocolVersion)
		Example(toolregistry.WireProtocolVersion)
	})
	Required("name", "tools", "provider_id", "admission_revision", "provider_incarnation_id", "wire_protocol_version")
})

var RegisterResult = Type("RegisterResult", func() {
	Description("Result of a successful toolset registration")
	Field(1, "registered_at", String, "ISO 8601 timestamp of registration", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Field(2, "registration_token", String, "Deterministic admission-generation token derived from the wire protocol version, canonical schema fingerprint, and deployment-issued admission revision", func() {
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
		Pattern(`^[^\x00]+$`)
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

var DrainProviderPayload = Type("DrainProviderPayload", func() {
	Description("Exact provider lease and configured settlement lifecycle for beginning drain.")
	Extend(ReleaseProviderPayload)
	Field(100, "settlement_duration_ms", Int64, "Full provider shutdown duration for which the draining lease must retain settlement authority.", func() {
		Minimum(1)
		Maximum(toolregistry.MaxProviderLeaseDuration.Milliseconds())
		Example(30000)
	})
	Required("settlement_duration_ms")
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
		Pattern(`^[^\x00]+$`)
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
	Field(5, "wire_protocol_version", Int, "Required runtime-owned version of the consumer message envelope. The registry accepts only its exact canonical version.", func() {
		Enum(toolregistry.WireProtocolVersion)
		Example(toolregistry.WireProtocolVersion)
	})
	Required("toolset", "tool", "payload_json", "meta", "wire_protocol_version")
})

var CallToolResult = Type("CallToolResult", func() {
	Description("Routing contract for awaiting one registry-routed call through its execution deadline while retaining the canonical result until the later stream expiration.")
	Field(1, "tool_use_id", String, "Global transport identifier derived from required run_id and tool_call_id.", func() {
		MinLength(1)
		MaxLength(256)
		Example("call-abc123")
	})
	Field(2, "registration_token", String, "Exact admission-generation token stamped on the routed call", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Field(3, "execution_deadline", String, "Absolute Redis-owned deadline that bounds provider execution and caller waiting.", func() {
		Format(FormatDateTime)
		Example("2026-08-05T10:10:00Z")
	})
	Field(4, "result_stream_expires_at", String, "Later absolute Redis-owned expiration shared by the call record and result stream.", func() {
		Format(FormatDateTime)
		Example("2026-08-05T10:15:00Z")
	})
	Required("tool_use_id", "registration_token", "execution_deadline", "result_stream_expires_at")
})

var RetryToolPayload = Type("RetryToolPayload", func() {
	Description("Runtime-owned identity and immutable request for retrying one admitted execution after provider overload.")
	Extend(CallToolPayload)
	Field(100, "expected_registration_token", String, "Exact admission-generation token returned by the original CallTool admission.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Required("expected_registration_token")
})

var CompleteToolCallPayload = Type("CompleteToolCallPayload", func() {
	Description("Exact provider lease and canonical terminal result for one admitted tool call.")
	Field(1, "toolset", String, "Toolset whose provider completed the call.", func() {
		MinLength(1)
		MaxLength(256)
		Example("atlas_data.atlas.read")
	})
	Field(2, "provider_id", String, "Stable provider process identity that executed the call.", func() {
		MinLength(1)
		MaxLength(512)
		Pattern(`^[^\x00]+$`)
		Example("atlas-data-7cd8949c8f-k2nrp/atlas_data.atlas.read")
	})
	Field(3, "provider_incarnation_id", String, "Runtime UUID of the exact Serve lifecycle that executed the call.", func() {
		Format(FormatUUID)
		Example("8af45fe9-5c32-4b46-8da5-d350e98b68f3")
	})
	Field(4, "registration_token", String, "Exact admission-generation token stamped on the call.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Field(5, "tool_use_id", String, "Global transport identity stamped on the call.", func() {
		Pattern(toolregistry.ToolUseIDPattern)
		Example("5c1d91e7ea6a1aa1bb3c395e0a7e09901a85df66fb064a679d6f0ff0d12a516e")
	})
	Field(6, "result_json", Bytes, "Canonical encoded terminal ToolResultMessage.", func() {
		MinLength(1)
		Example([]byte(`{"registration_token":"270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c","tool_use_id":"5c1d91e7ea6a1aa1bb3c395e0a7e09901a85df66fb064a679d6f0ff0d12a516e","result_json":{"ok":true}}`))
	})
	Field(7, "request_event_id", String, "Pulse request-stream event claimed by this provider.", func() {
		Pattern(`^\d+-\d+$`)
		Example("1721736123456-0")
	})
	Field(8, "provider_registration_token", String, "Exact registration token of the provider lease settling the claim.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("7ddaeccbe5b9c901a2773fc77097f7970669988ea6dfca6cb3205ffcd552cc82")
	})
	Required("toolset", "provider_id", "provider_incarnation_id", "registration_token", "tool_use_id", "result_json", "request_event_id", "provider_registration_token")
})

var ProviderToolCallClaimPayload = Type("ProviderToolCallClaimPayload", func() {
	Description("Exact provider lease and Pulse claim for one admitted tool call.")
	Field(1, "toolset", String, "Toolset whose provider claimed the call.", func() {
		MinLength(1)
		MaxLength(256)
		Example("atlas_data.atlas.read")
	})
	Field(2, "provider_id", String, "Stable identity of the provider process.", func() {
		MinLength(1)
		MaxLength(512)
		Pattern(`^[^\x00]+$`)
		Example("atlas-data-7cd8949c8f-k2nrp/atlas_data.atlas.read")
	})
	Field(3, "provider_incarnation_id", String, "Runtime UUID of the exact Serve lifecycle.", func() {
		Format(FormatUUID)
		Example("8af45fe9-5c32-4b46-8da5-d350e98b68f3")
	})
	Field(4, "provider_registration_token", String, "Exact registration token of the provider lease.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("7ddaeccbe5b9c901a2773fc77097f7970669988ea6dfca6cb3205ffcd552cc82")
	})
	Field(5, "call_registration_token", String, "Admission token stamped on the claimed call.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("270a659d38ff331401280ad7b0c8fdba673fd02e7114b856a2f12e1c49eec34c")
	})
	Field(6, "tool_use_id", String, "Global transport identity stamped on the claimed call.", func() {
		Pattern(toolregistry.ToolUseIDPattern)
		Example("5c1d91e7ea6a1aa1bb3c395e0a7e09901a85df66fb064a679d6f0ff0d12a516e")
	})
	Field(7, "request_event_id", String, "Pulse request-stream event claimed by this provider.", func() {
		Pattern(`^\d+-\d+$`)
		Example("1721736123456-0")
	})
	Required(
		"toolset",
		"provider_id",
		"provider_incarnation_id",
		"provider_registration_token",
		"call_registration_token",
		"tool_use_id",
		"request_event_id",
	)
})

var PublishToolOutputDeltaPayload = Type("PublishToolOutputDeltaPayload", func() {
	Description("Exact provider claim and one best-effort output fragment.")
	Extend(ProviderToolCallClaimPayload)
	Field(100, "stream", String, "Logical output stream such as stdout or stderr.", func() {
		MinLength(1)
		MaxLength(128)
		Pattern(`^[^\x00]+$`)
		Example("stdout")
	})
	Field(101, "delta", String, "Output fragment emitted by the running tool.", func() {
		MinLength(1)
		MaxLength(toolregistry.MaxToolOutputDeltaBytes)
		Example("processed 10 rows\n")
	})
	Required("stream", "delta")
})

var ClaimToolCallResult = Type("ClaimToolCallResult", func() {
	Description("Authoritative pre-dispatch disposition for one queued tool call.")
	Field(1, "disposition", String, "Closed settlement outcome. execute grants immutable dispatch ownership; terminal means retained terminal history already exists; claimed means another request delivery owns execution; expired means Redis time settled the call.", func() {
		Enum("execute", "terminal", "claimed", "expired")
		Example("execute")
	})
	Required("disposition")
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
