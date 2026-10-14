// This file defines the toolset definitions returned by registry discovery.
// Applications reuse these schemas to retain a selected definition and its exact
// registration without importing or registering the registry service design.
package types

import (
	"goa.design/goa-ai/runtime/toolregistry"
	. "goa.design/goa/v3/dsl"
)

// Toolset describes a complete registered toolset and its tool schemas.
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

// ResolvedToolset pairs the selected definition with the registration required to call it.
var ResolvedToolset = Type("ResolvedToolset", func() {
	Description("One active toolset definition and the registration that supplied it.")
	Field(1, "toolset", Toolset, "Complete toolset definition read from the active registration.")
	Field(2, "registration_token", String, "Exact registration required when executing a call selected from this definition.", func() {
		Pattern(toolregistry.RegistrationTokenPattern)
		Example("1111111111111111111111111111111111111111111111111111111111111111")
	})
	Required("toolset", "registration_token")
})
