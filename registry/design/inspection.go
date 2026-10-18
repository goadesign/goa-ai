// Registry inspection returns descriptive fields and JSON schemas independently
// of executable consumer metadata. Existing declaration types own constraints.
package design

import (
	registrytypes "goa.design/goa-ai/registry/design/types"
	. "goa.design/goa/v3/dsl"
)

// ToolsetSchemas describes the active saved toolset without execution authority.
var ToolsetSchemas = Type("ToolsetSchemas", func() {
	Description("Descriptive fields and schemas from one active saved toolset. Consumer metadata and registration authority are intentionally absent.")
	Reference(registrytypes.Toolset)
	Field(1, "name", String, "Unique name of the saved toolset.")
	Field(2, "description", String, "Human-readable description.")
	Field(3, "version", registrytypes.SemVer, "Semantic version of the toolset.")
	Field(4, "tags", ArrayOf(String), "Tags for categorization.")
	Field(5, "tools", ArrayOf(ToolSchemas), "Saved schemas and descriptive fields for each tool.")
	Field(6, "registered_at", String, "Time the active registration was saved.")
	Required("name", "tools", "registered_at")
})

// ToolSchemas describes a tool's input and output shapes for inspection.
var ToolSchemas = Type("ToolSchemas", func() {
	Description("Saved argument, result, and optional sidecar schemas with their original descriptive fields. This type is not an executable tool declaration.")
	Reference(registrytypes.ToolSchema)
	Field(1, "name", String, "Original full tool identifier.")
	Field(2, "description", String, "Human-readable description of the tool.")
	Field(3, "tags", ArrayOf(String), "Tool categorization tags.")
	Field(4, "payload_schema", Bytes, "Original JSON schema for model-supplied arguments.")
	Field(5, "result_schema", Bytes, "Original JSON schema for the result.")
	Field(6, "sidecar_schema", Bytes, "Original JSON schema for optional sidecar output.")
	Required("name", "payload_schema", "result_schema")
})
