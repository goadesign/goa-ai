// Package design gives the runtime generated decoders for discovery records
// and known Agent Skills frontmatter fields. Unknown frontmatter fields remain
// in the original entry and are compared with YAML before instructions load.
package design

import (
	_ "goa.design/goa-ai/dsl"
	"goa.design/goa-ai/internal/mcpskills/schema"
	. "goa.design/goa/v3/dsl"
)

var _ = API("mcp_skills", func() {
	Description("Decode Skill discovery records before their manifests or instructions enter a host.")
})

var _ = Service("skill_entries", func() {
	Description("Validate MCP Skill entries and Agent Skills fields for generated servers and consuming hosts.")
	Method("decode", func() {
		Description("Decode one complete Skill entry before verifying its directory membership and frontmatter.")
		Payload(schema.Entry())
		HTTP(func() { POST("/entry") })
	})
	Method("decode_frontmatter", func() {
		Description("Validate known frontmatter fields while retaining the original open JSON object separately for comparison with served YAML.")
		Payload(func() {
			Field(1, "name", String, "Skill label matching its directory name", func() {
				MinLength(1)
				MaxLength(64)
				Pattern(`^[\p{L}\p{N}]+(-[\p{L}\p{N}]+)*$`)
			})
			Field(2, "description", String, "What this skill does and when to use it", func() { MinLength(1); MaxLength(1024) })
			Field(3, "license", String, "License name or reference to a license file")
			Field(4, "compatibility", String, "Environment requirements", func() { MinLength(1); MaxLength(500) })
			Field(5, "metadata", MapOf(String, String), "Authored string metadata")
			Field(6, "allowed-tools", String, "Requested tools; this declaration does not grant permission")
			Required("name", "description")
		})
		HTTP(func() { POST("/frontmatter") })
	})
})
