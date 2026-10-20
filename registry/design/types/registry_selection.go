// Saved registry selections retain the exact provider declaration, or for a tool
// compiled into the consumer, only the registration facts chosen for the call.
// Direct imports keep original names; registered resources also retain the
// identity from their owner. Compiled selections rely on the consumer's
// generated declarations and a fingerprint that detects a changed build.
package types

import (
	"goa.design/goa-ai/runtime/toolregistry"
	. "goa.design/goa/v3/dsl"
)

var (
	// RegisteredToolsetSelection pairs an owner-selected resource with its exact declaration.
	RegisteredToolsetSelection = Type("RegisteredToolsetSelection", func() {
		Field(1, "source", CatalogSource, "Owner-selected resource and exact registration target.")
		Field(2, "resolved", ResolvedToolset, "Original operation declarations and registration token.")
		Required("source", "resolved")
	})
	// CompiledToolSelection records which registration was selected for a call to
	// a tool whose declaration is generated into the consuming agent. It holds
	// the facts that vary per registration; the consumer supplies the operation
	// declarations from generated code and compares the fingerprint first.
	CompiledToolSelection = Type("CompiledToolSelection", func() {
		Description("Registration selected for a call to a tool compiled into the consumer.")
		Field(1, "registration_token", String, "Exact registration selected for the call.", func() {
			Pattern(toolregistry.RegistrationTokenPattern)
			Example("1111111111111111111111111111111111111111111111111111111111111111")
		})
		Field(2, "name", String, "Registered toolset name.", func() {
			MinLength(1)
			MaxLength(256)
			Example("data-tools")
		})
		Field(3, "description", String, "Registered toolset description.", func() {
			Example("Tools for data processing and analysis")
		})
		Field(4, "version", SemVer, "Registered toolset version.")
		Field(5, "tags", ArrayOf(String), "Registered toolset tags.", func() {
			Example([]string{"data", "etl"})
		})
		Field(6, "registered_at", String, "ISO 8601 time the registry accepted the registration.", func() {
			Format(FormatDateTime)
			Example("2024-01-15T10:30:00Z")
		})
		Field(7, "operations", ArrayOf(String), "Names of the called operation and its paging partner, sorted.", func() {
			MinLength(1)
			Example([]string{"data.find", "data.find_next"})
		})
		Field(8, "declaration_fingerprint", String, "SHA-256 of the generated declarations of the listed operations, without provider Agent targets.", func() {
			Pattern(`^[0-9a-f]{64}$`)
			Example("2222222222222222222222222222222222222222222222222222222222222222")
		})
		Required("registration_token", "name", "registered_at", "operations", "declaration_fingerprint")
	})
	// CompiledAgentToolSelection adds the provider target selected for a native
	// Agent tool. The registration supplies this target; readers still check its
	// route against the consumer's generated child route.
	CompiledAgentToolSelection = Type("CompiledAgentToolSelection", func() {
		Description("Registration and provider target selected for a call to a compiled Agent tool.")
		Field(1, "selection", CompiledToolSelection, "Registration selected for the call.")
		Field(2, "agent", AgentToolTarget, "Provider worker route and configuration selected for the call.")
		Required("selection", "agent")
	})
	// RegistrySelection retains one of the supported registry import contracts.
	RegistrySelection = Type("RegistrySelection", func() {
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		OneOf("selection", func() {
			TypeName("RegistrySelectionKind")
			Field(1, "direct", ResolvedToolset, "Direct import using original operation names.")
			Field(2, "registered", RegisteredToolsetSelection, "Owner-selected resource with derived operation names.")
			Field(3, "compiled_service", CompiledToolSelection, "Service tool compiled into the consumer.")
			Field(4, "compiled_agent", CompiledAgentToolSelection, "Native Agent tool compiled into the consumer.")
		})
		Required("selection")
	})
)
