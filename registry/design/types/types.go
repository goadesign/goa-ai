// Package types exports the registry's Goa schemas without registering an API or
// service. Applications can reuse the same tool declarations, execution context,
// and validation in their own Goa designs.
package types

import (
	"goa.design/goa-ai/runtime/toolregistry"
	. "goa.design/goa/v3/dsl"
)

// SemVer validates a semantic version used in tool discovery.
var SemVer = Type("SemVer", String, func() {
	Description("Semantic version string (for example, \"1.0.0\" or \"v1.0.0\").")
	Pattern(`^v?\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?$`)
	Example("1.0.0")
})

// ToolCallMeta describes execution context supplied with a tool invocation.
var ToolCallMeta = Type("ToolCallMeta", func() {
	Description("Context metadata propagated alongside tool calls for routing, correlation, and domain injection (for example, session-scoped data access).")
	Field(1, "run_id", String, "Run identifier for the agent execution that issued this tool call.", func() {
		MinLength(1)
		MaxLength(toolregistry.MaxToolCallMetaIDLength)
		Pattern(`^[^\x00]+$`)
		Example("run_01J3K9Q9T6E2G7N0G2ZQH2KX1A")
	})
	Field(2, "session_id", String, "Agent session identifier used to scope tool behavior and persistence.", func() {
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
	Field(6, "labels", MapOf(String, String), "Run labels and runtime-supplied values fixed for this call. Providers use them to fill fields declared with Inject; models never see them.", func() {
		Example(map[string]string{"site_id": "site-123"})
	})
	Required("run_id", "session_id", "tool_call_id")
})

// ProviderToolCallClaimPayload identifies the provider lease and claimed invocation.
var ProviderToolCallClaimPayload = Type("ProviderToolCallClaimPayload", func() {
	Description("Exact provider lease and Pulse claim for one admitted tool call.")
	Field(1, "toolset", String, "Toolset whose provider claimed the call.", func() {
		MinLength(1)
		MaxLength(256)
		Example("catalog.lookup")
	})
	Field(2, "provider_id", String, "Stable identity of the provider process.", func() {
		MinLength(1)
		MaxLength(512)
		Pattern(`^[^\x00]+$`)
		Example("catalog-provider/catalog.lookup")
	})
	Field(3, "provider_incarnation_id", String, "Runtime UUID of the exact Serve lifecycle.", func() {
		Format(FormatUUID)
		Example("00000000-0000-4000-8000-000000000001")
	})
	Field(4, "provider_registration_token", String, "Exact registration token of the provider lease.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("2222222222222222222222222222222222222222222222222222222222222222")
	})
	Field(5, "call_registration_token", String, "Admission token stamped on the claimed call.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Field(6, "tool_use_id", String, "Global transport identity stamped on the claimed call.", func() {
		Pattern(toolregistry.ToolUseIDPattern)
		Example("3333333333333333333333333333333333333333333333333333333333333333")
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

// PublishToolOutputDeltaPayload associates an output fragment with its claimed invocation.
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

// ToolSchema declares one tool's inputs, results, and execution requirements.
var ToolSchema = Type("ToolSchema", func() {
	Description("Tool schema declaration for registration with the tool registry gateway.")
	Field(1, "name", String, "Globally unique tool identifier of the form \"toolset.tool\".", func() {
		MinLength(1)
		MaxLength(256)
		Example("catalog.lookup.find_records")
	})
	Field(2, "description", String, "Human-readable description of what the tool does.", func() {
		Example("Find records that match a catalog query.")
	})
	Field(3, "tags", ArrayOf(String), "Optional tags used for policy, routing, or UI filtering.", func() {
		Example([]string{"catalog", "records", "read"})
	})
	Field(4, "payload_schema", Bytes, "Canonical JSON schema for arguments accepted from the model.", func() {
		MinLength(1)
		Example([]byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`))
	})
	Field(7, "execution_payload_schema", Bytes, "Canonical JSON schema for the payload sent to the provider. It includes fields supplied by continuation handling and excludes fields injected inside the provider.", func() {
		MinLength(1)
		Example([]byte(`{"type":"object","properties":{"query":{"type":"string"},"cursor":{"type":"string"}},"required":["query","cursor"]}`))
	})
	Field(5, "result_schema", Bytes, "Canonical JSON schema for the tool result.", func() {
		MinLength(1)
		Example([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`))
	})
	Field(6, "sidecar_schema", Bytes, "Canonical JSON schema for the tool sidecar (UI-only), when present.", func() {
		Example([]byte(`{"type":"object","properties":{"artifact_kind":{"type":"string"}}}`))
	})
	Field(8, "consumer_contract", ConsumerContract, "Generated contract needed to consume this tool without a compiled Go dependency. Schema-only declarations remain usable by static consumers; dynamic consumers require this complete contract.")
	Required("name", "payload_schema", "execution_payload_schema", "result_schema")
})

// AgentToolsetDeclaration groups native Agent tools and their immutable targets.
var AgentToolsetDeclaration = Type("AgentToolsetDeclaration", func() {
	Description("Named collection of native Agent tools and their immutable targets.")
	Field(1, "name", String, "Unique toolset name.", func() {
		MinLength(1)
		Example("installation")
	})
	Field(2, "description", String, "Description of this toolset.")
	Field(3, "version", SemVer, "Semantic version used by discovery filters.")
	Field(4, "tags", ArrayOf(String), "Application categories used by discovery filters.")
	Field(5, "tools", ArrayOf(ToolSchema), "Complete Agent tool declarations.", func() { MinLength(1) })
	Required("name", "tools")
})
