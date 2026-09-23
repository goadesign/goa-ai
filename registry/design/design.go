// Package design defines the internal tool registry service using Goa DSL.
// The registry acts as both a catalog and gateway — agents discover toolsets
// through the registry and invoke tools through it.
package design

import (
	. "goa.design/goa/v3/dsl"

	registrytypes "goa.design/goa-ai/registry/design/types"
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
	Error("call_not_admitted", ErrorResult, "The registry chose a rejected decision for this tool-use identity before provider publication, so no exact retry can execute while the run-scoped decision is retained")
	Error("admission_blocked", ErrorResult, "Another admission still has active provider leases")
	Error("admission_retired", ErrorResult, "The requested admission was intentionally retired")
	Error("admission_conflict", ErrorResult, "The expected admission token does not match the catalog record")
	Error("provider_lease_lost", ErrorResult, "The exact provider incarnation no longer holds the admitted lease and must stop serving")

	// gRPC transport configuration
	GRPC(func() {
		Response("not_found", CodeNotFound)
		Response("validation_error", CodeInvalidArgument)
		Response("service_unavailable", CodeUnavailable)
		Response("call_not_admitted", CodeUnavailable)
		Response("admission_blocked", CodeUnavailable)
		Response("admission_retired", CodeFailedPrecondition)
		Response("admission_conflict", CodeFailedPrecondition)
		Response("provider_lease_lost", CodeFailedPrecondition)
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
		Description("Reject providers whose required runtime-owned wire protocol version differs from the registry, then atomically admit one provider-incarnation lease in the catalog admission record. The same wire version, schema, and admission revision add or renew replicas under one token. A different token replaces the admission after Redis-time pruning proves every old lease expired and atomically tombstones the prior token; otherwise admission_blocked asks the provider to retry. Any candidate in the permanent retired-token set returns admission_retired and cannot resurrect. An already-draining incarnation returns provider_lease_lost; full registration cannot reopen it. Active providers use RenewProvider without resending definitions.")
		Payload(RegisterPayload)
		Result(RegisterResult)
		Error("admission_blocked")
		Error("admission_retired")
		Error("provider_lease_lost")
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("RenewProvider", func() {
		Description("Extend only the exact current unexpired provider-incarnation lease without reading or writing tool definitions. Preserve its registration token, health, draining status, and any longer settlement deadline. A missing, expired, replaced, or retired lease returns provider_lease_lost; renewal never creates admission authority. Infrastructure failures are retryable only within the provider's existing lease cutoff.")
		Payload(RenewProviderPayload)
		Result(RenewProviderResult)
		Error("provider_lease_lost")
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
		Description("Retire the exact current registration and remove it from discovery. Repeating the same-token retirement succeeds; a stale token returns admission_conflict. For service tools, preserve provider leases until graceful release or expiry and permanently prevent that token from registering again. Native Agent declarations have no provider leases and can be reactivated through ReplaceAgentToolset; already accepted child calls retain their selected declaration.")
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

	Method("RegisterAgentToolset", func() {
		Description("Create a native Agent toolset without a Pulse provider lease. Repeating the same active declaration succeeds. A different existing declaration returns admission_conflict; use ReplaceAgentToolset with its current token.")
		Payload(registrytypes.AgentToolsetDeclaration)
		Result(ResolvedToolset)
		Error("admission_conflict")
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("ReplaceAgentToolset", func() {
		Description("Replace or reactivate a native Agent toolset only when the current registration matches expected_registration_token. Already accepted child calls retain their original declarations. New discovery returns the replacement.")
		Payload(func() {
			Extend(registrytypes.AgentToolsetDeclaration)
			Field(100, "expected_registration_token", String, "Current native Agent registration being replaced.", func() {
				Pattern(toolregistry.RegistrationTokenPattern)
			})
			Required("expected_registration_token")
		})
		Result(ResolvedToolset)
		Error("admission_conflict")
		Error("validation_error")
		Error("service_unavailable")
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

	Method("ResolveToolset", func() {
		Description("Return a toolset definition and its exact registration token from one active catalog read. Consumers retain the token with accepted calls so a later provider replacement cannot change the contract selected by the model. Resolution describes registered tools; it does not guarantee provider health.")
		Payload(GetToolsetPayload)
		Result(ResolvedToolset)
		Error("not_found")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("CheckAdmission", func() {
		Description("Report whether the exact registration token derived from a deployed provider's generated tool schemas is active and currently has an unexpired, non-draining provider lease plus a fresh authenticated pong. Release verification calls this after workload rollout because Kubernetes proves the intended pods are running while the registry proves their exact tool contract is routable. A missing or different active admission returns ready=false rather than an error.")
		Payload(CheckAdmissionPayload)
		Result(AdmissionStatus)
		Error("service_unavailable")
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
		Description("Reject consumers whose required runtime-owned wire protocol version differs from the registry, then attach or create one run-scoped tool-call record. A valid unpublished call waits for a healthy provider within its original execution deadline. Request publication atomically verifies that the selected registration remains current and non-draining; if it changed, the unpublished call selects the replacement and tries again without extending its deadline. The provider assignment becomes immutable when publication commits. Certain pre-publication failures commit call_not_admitted so exact retries cannot execute, while published calls never transfer because an external effect may have begun. The record retains the full canonical terminal through its absolute expiration and restores it when bounded result-stream history was trimmed. Before returning a published admission, the registry establishes the result stream so the caller can create a reader immediately.")
		Payload(CallToolPayload)
		Result(CallToolResult)
		Error("not_found")
		Error("validation_error")
		Error("service_unavailable")
		Error("call_not_admitted")
		GRPC(func() {})
	})

	Method("CallResolvedTool", func() {
		Description("Admit a tool call only against the exact registration returned by ResolveToolset. The token is part of the immutable call identity. A replacement before publication commits call_not_admitted; published calls keep their original assignment and result. Repeating this operation attaches to the same call, or resumes its authoritative provider-overload event against the same registration and original deadline.")
		Payload(CallResolvedToolPayload)
		Result(CallToolResult)
		Error("not_found")
		Error("validation_error")
		Error("service_unavailable")
		Error("call_not_admitted")
		Error("admission_conflict")
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
		Payload(registrytypes.PublishToolOutputDeltaPayload)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("ReportToolCallOverload", func() {
		Description("Report that an exact provider claim could not enter its bounded worker queue. The registry verifies the provider lease and request-event claim, then atomically appends retry control only while the authoritative call record remains nonterminal.")
		Payload(registrytypes.ProviderToolCallClaimPayload)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("ClaimToolCall", func() {
		Description("Atomically decide whether one queued request may enter handler execution. An active provider gains immutable ownership, and an exact replay of the same claim operation returns the same execute decision so an uncertain transport result can be retried safely. A Pulse redelivery starts a different claim operation and therefore cannot repeat handler execution. A different owner, retained terminal history, and Redis-owned expiration settle without execution. A draining, expired, or retired lease is rejected without changing unclaimed work, while a request authored under a stale registration receives the canonical stale-generation terminal. Only the exact granted provider incarnation and request event may publish deltas or complete the call; ownership never transfers after a crash.")
		Idempotent()
		Payload(ClaimToolCallPayload)
		Result(ClaimToolCallResult)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})
})

// ---- Payload and Result Types ----

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
	Field(3, "version", registrytypes.SemVer, "Semantic version of the toolset.")
	Field(4, "tags", ArrayOf(String), "Tags for categorization and filtering", func() {
		Example([]string{"data", "etl", "analytics"})
	})
	Field(5, "tools", ArrayOf(registrytypes.ToolSchema), "Tool definitions with their schemas")
	Field(6, "provider_id", String, "Stable identity of the provider process registering this toolset.", func() {
		MinLength(1)
		MaxLength(512)
		Pattern(`^[^\x00]+$`)
		Example("catalog-provider/catalog.lookup")
	})
	Field(7, "admission_revision", String, "Deployment-issued revision shared by every replica of one fenced admission. Reuse it for same-contract scaling and rolling updates; change it only to create a new fenced admission.", func() {
		Pattern(toolregistry.AdmissionRevisionPattern)
		Example("example-release+schema-v1")
	})
	Field(8, "provider_incarnation_id", String, "Runtime-generated UUID identifying one Serve lifecycle. The provider runtime generates it once and reuses it for every renewal.", func() {
		Format(FormatUUID)
		Example("00000000-0000-4000-8000-000000000001")
	})
	Field(9, "wire_protocol_version", Int, "Required runtime-owned version of the provider message envelope. The registry admits only its exact canonical version.", func() {
		Enum(toolregistry.WireProtocolVersion)
		Example(toolregistry.WireProtocolVersion)
	})
	Field(10, "schema_fingerprint", String, "Generated lowercase SHA-256 identity of the exact tool schemas sent by this provider. The registry independently derives the identity and rejects mismatches before admission.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("name", "tools", "provider_id", "admission_revision", "provider_incarnation_id", "wire_protocol_version", "schema_fingerprint")
})

var RegisterResult = Type("RegisterResult", func() {
	Description("Result of a successful toolset registration")
	Field(1, "registered_at", String, "ISO 8601 timestamp of registration", func() {
		Format(FormatDateTime)
		Example("2024-01-15T10:30:00Z")
	})
	Field(2, "registration_token", String, "Deterministic admission-generation token derived from the wire protocol version, canonical schema fingerprint, and deployment-issued admission revision", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Field(3, "lease_duration_ms", Int64, "Duration of the admitted provider lease in milliseconds", func() {
		Minimum(1)
		Maximum(toolregistry.MaxProviderLeaseDuration.Milliseconds())
		Example(120000)
	})
	Required("registered_at", "registration_token", "lease_duration_ms")
})

var UnregisterPayload = Type("UnregisterPayload", func() {
	Description("Identify the exact current registration to retire.")
	Field(1, "name", String, "Name of the toolset to unregister", func() {
		MinLength(1)
		Example("data-tools")
	})
	Field(2, "expected_registration_token", String, "Current token returned by Register, RegisterAgentToolset, ReplaceAgentToolset, or ResolveToolset.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("name", "expected_registration_token")
})

var RenewProviderPayload = Type("RenewProviderPayload", func() {
	Description("Exact existing provider lease to renew, without tool definitions.")
	providerLeaseIdentityFields()
})

var RenewProviderResult = Type("RenewProviderResult", func() {
	Description("Duration of the renewed lease; admission identity remains unchanged.")
	Field(1, "lease_duration_ms", Int64, "Renewed provider lease duration in milliseconds", func() {
		Minimum(1)
		Maximum(toolregistry.MaxProviderLeaseDuration.Milliseconds())
		Example(120000)
	})
	Required("lease_duration_ms")
})

var ReleaseProviderPayload = Type("ReleaseProviderPayload", func() {
	Description("Exact provider lease release payload")
	providerLeaseIdentityFields()
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
		Example("catalog-provider/catalog.lookup")
	})
	Field(4, "provider_incarnation_id", String, "Runtime-generated UUID of the Serve lifecycle responding to the ping.", func() {
		Format(FormatUUID)
		Example("00000000-0000-4000-8000-000000000001")
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

var ResolvedToolset = Type("ResolvedToolset", func() {
	Description("One active toolset definition and the registration that supplied it.")
	Field(1, "toolset", Toolset, "Complete toolset definition read from the active registration.")
	Field(2, "registration_token", String, "Exact registration required when executing a call selected from this definition.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("toolset", "registration_token")
})

var CheckAdmissionPayload = Type("CheckAdmissionPayload", func() {
	Description("The exact toolset admission derived from one deployed provider's generated schemas, admission revision, and wire protocol.")
	Field(1, "name", String, "Name of the toolset whose admission must be checked.", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Field(2, "expected_registration_token", String, "Deterministic token derived from the deployed provider's generated schema, admission revision, and wire protocol.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("name", "expected_registration_token")
})

var AdmissionStatus = Type("AdmissionStatus", func() {
	Description("Authoritative routing readiness for one exact expected admission.")
	Field(1, "ready", Boolean, "True only when the expected registration token is active and has a routable provider plus a fresh authenticated pong.")
	Required("ready")
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
	Field(1, "toolset", String, "Toolset registration identifier used for routing (for example, \"catalog.lookup\").", func() {
		MinLength(1)
		MaxLength(256)
		Example("catalog.lookup")
	})
	Field(2, "tool", String, "Globally unique tool identifier of the form \"toolset.tool\" (for example, \"catalog.lookup.find_records\").", func() {
		MinLength(1)
		MaxLength(256)
		Example("catalog.lookup.find_records")
	})
	Field(3, "payload_json", Bytes, "Canonical JSON payload for the tool call. Must validate against the registered payload schema.", func() {
		MinLength(1)
		Example([]byte(`{"query":"recent orders"}`))
	})
	Field(4, "meta", registrytypes.ToolCallMeta, "Execution metadata propagated alongside the tool call.")
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
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Field(3, "execution_deadline", String, "Absolute Redis-owned deadline that bounds provider execution and caller waiting.", func() {
		Format(FormatDateTime)
		Example("2025-01-15T10:10:00Z")
	})
	Field(4, "result_stream_expires_at", String, "Later absolute Redis-owned expiration shared by the call record and result stream.", func() {
		Format(FormatDateTime)
		Example("2025-01-15T10:15:00Z")
	})
	Required("tool_use_id", "registration_token", "execution_deadline", "result_stream_expires_at")
})

var RetryToolPayload = Type("RetryToolPayload", func() {
	Description("Runtime-owned identity and immutable request for retrying one admitted execution after provider overload.")
	Extend(CallToolPayload)
	Field(100, "expected_registration_token", String, "Exact admission-generation token returned by the original CallTool admission.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("expected_registration_token")
})

var CallResolvedToolPayload = Type("CallResolvedToolPayload", func() {
	Description("Immutable tool call selected from one resolved registration.")
	Extend(CallToolPayload)
	Field(100, "expected_registration_token", String, "Exact registration returned with the definition used to select this call.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("expected_registration_token")
})

var CompleteToolCallPayload = Type("CompleteToolCallPayload", func() {
	Description("Exact provider lease and canonical terminal result for one admitted tool call.")
	Field(1, "toolset", String, "Toolset whose provider completed the call.", func() {
		MinLength(1)
		MaxLength(256)
		Example("catalog.lookup")
	})
	Field(2, "provider_id", String, "Stable provider process identity that executed the call.", func() {
		MinLength(1)
		MaxLength(512)
		Pattern(`^[^\x00]+$`)
		Example("catalog-provider/catalog.lookup")
	})
	Field(3, "provider_incarnation_id", String, "Runtime UUID of the exact Serve lifecycle that executed the call.", func() {
		Format(FormatUUID)
		Example("00000000-0000-4000-8000-000000000001")
	})
	Field(4, "registration_token", String, "Exact admission-generation token stamped on the call.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Field(5, "tool_use_id", String, "Global transport identity stamped on the call.", func() {
		Pattern(toolregistry.ToolUseIDPattern)
		Example("3333333333333333333333333333333333333333333333333333333333333333")
	})
	Field(6, "result_json", Bytes, "Canonical encoded terminal ToolResultMessage.", func() {
		MinLength(1)
		Example([]byte(`{"registration_token":"1111111111111111111111111111111111111111111111111111111111111111","tool_use_id":"3333333333333333333333333333333333333333333333333333333333333333","result_json":{"ok":true}}`))
	})
	Field(7, "request_event_id", String, "Pulse request-stream event claimed by this provider.", func() {
		Pattern(`^\d+-\d+$`)
		Example("1721736123456-0")
	})
	Field(8, "provider_registration_token", String, "Exact registration token of the provider lease settling the claim.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("2222222222222222222222222222222222222222222222222222222222222222")
	})
	Required("toolset", "provider_id", "provider_incarnation_id", "registration_token", "tool_use_id", "result_json", "request_event_id", "provider_registration_token")
})

var ClaimToolCallPayload = Type("ClaimToolCallPayload", func() {
	Description("Exact provider claim operation for one request event. Transport retries reuse the operation ID; a later Pulse redelivery uses a new ID.")
	Extend(registrytypes.ProviderToolCallClaimPayload)
	Field(100, "claim_operation_id", String, "Runtime UUID created once for this claim operation and reused by its transport retries.", func() {
		Format(FormatUUID)
		Example("00000000-0000-4000-8000-000000000002")
	})
	Required("claim_operation_id")
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
	Field(3, "version", registrytypes.SemVer, "Semantic version of the toolset.")
	Field(4, "tags", ArrayOf(String), "Tags for categorization", func() {
		Example([]string{"data", "etl"})
	})
	Field(5, "tools", ArrayOf(registrytypes.ToolSchema), "Tool schemas included in the toolset.")
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
	Field(3, "version", registrytypes.SemVer, "Semantic version of the toolset.")
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

// providerLeaseIdentityFields keeps lease operations on the same exact identity.
func providerLeaseIdentityFields() {
	Field(1, "name", String, "Name of the registered toolset", func() {
		MinLength(1)
		MaxLength(256)
		Example("data-tools")
	})
	Field(2, "provider_id", String, "Stable identity of the provider process", func() {
		MinLength(1)
		MaxLength(512)
		Pattern(`^[^\x00]+$`)
		Example("catalog-provider/catalog.lookup")
	})
	Field(3, "expected_registration_token", String, "Exact admission-generation token returned by Register", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Field(4, "provider_incarnation_id", String, "Runtime-generated UUID of the exact Serve lifecycle.", func() {
		Format(FormatUUID)
		Example("00000000-0000-4000-8000-000000000001")
	})
	Required("name", "provider_id", "expected_registration_token", "provider_incarnation_id")
}
