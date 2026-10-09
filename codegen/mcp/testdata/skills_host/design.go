// Package design defines a synthetic instruction server and a host's local
// execution tool. The host reuses generated clients and runtime confirmation;
// remote frontmatter never supplies permissions or an execution endpoint.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("skill-host", func() {
	Description("Verify generated Skill discovery and a host that owns consent and model context")
})

var file = Type("File", func() {
	Field(1, "uri", String, "Exact file resource URI", func() { Format(FormatURI) })
	Field(2, "digest", String, "SHA-256 of raw file bytes", func() { Pattern(`^sha256:[0-9a-f]{64}$`) })
	Field(3, "size", Int64, "Raw file byte length", func() { Minimum(0) })
	Required("uri", "digest", "size")
})

var entry = Type("Entry", func() {
	Field(1, "uri", String, "Exact Skill entry URI", func() { Format(FormatURI) })
	Field(2, "frontmatter", Any, "Every authored YAML field", func() {
		Meta("struct:field:type", "json.RawMessage", "encoding/json")
	})
	OneOf("resources", "Complete files or generated content", func() {
		TypeName("Files")
		Meta("oneof:json:untagged")
		Attribute("manifest", ArrayOfRequired(file), "Every Skill file")
		Attribute("dynamic", String, "Content without stable digests", func() { Enum("dynamic") })
	})
	Required("uri", "frontmatter", "resources")
})

var text = Type("Text", func() {
	Field(1, "uri", String, "Exact resource URI", func() { Format(FormatURI) })
	Field(2, "text", String, "Original UTF-8 file contents")
	Required("uri", "text")
})

var item = Type("Item", func() {
	OneOf("content", "One resource representation", func() { Attribute("text", text, "Original text") })
	Required("content")
})

var _ = Service("instructions", func() {
	Description("Serve complete Skill entries and individual files without activating instructions")
	MCP("instructions", "1")
	JSONRPC(func() { POST("/mcp") })
	Method("list", func() {
		Description("List complete entries without reading any file")
		Payload(func() { Field(1, "cursor", String, "Opaque next-page cursor") })
		Result(func() {
			Field(1, "skills", ArrayOfRequired(entry), "Complete entries in this page")
			Field(2, "nextCursor", String, "Opaque next-page cursor")
		})
		SkillCatalog()
	})
	Method("lookup", func() {
		Description("Resolve an exact Skill URI even when it is absent from the listing")
		Payload(func() {
			Field(1, "uri", String, "Exact Skill URI", func() { Format(FormatURI) })
			Required("uri")
		})
		Error("invalid_params", func() { Description("The requested Skill is unknown") })
		Result(func() { Field(1, "skill", entry, "Complete entry"); Required("skill") })
		SkillLookup()
	})
	Method("read", func() {
		Description("Read one requested file without interpreting its instructions")
		Payload(func() {
			Field(1, "uri", String, "Exact file URI", func() { Format(FormatURI) })
			Required("uri")
		})
		Error("invalid_params", func() { Description("The requested file is unknown") })
		Result(func() { Field(1, "contents", ArrayOfRequired(item), "Requested file contents") })
		ResourceReader()
	})
})

// The discovery extension map remains open. Its individual declarations use
// generated typed decoding so another extension cannot change Skill support.
var extension = Type("Extension", Any, func() {
	Description("One open extension declaration")
	Meta("struct:field:type", "json.RawMessage", "encoding/json")
})
var extensions = Type("Extensions", MapOf(String, extension))
var skillDeclaration = Type("SkillDeclaration", func() {
	Field(1, "directoryRead", Boolean, "Whether the server implements directory listing")
})

var _ = Service("host_shapes", func() {
	Description("Decode extension declarations using generated contracts before the host calls optional protocol methods")
	Method("extensions", func() { Payload(extensions) })
	Method("skill_declaration", func() { Payload(skillDeclaration) })
})

var _ = Service("host_actions", func() {
	Description("Execute only a listed script under consent bound to its retained Skill manifest")
	Method("execute", func() {
		Description("Run a verified Skill script after the host obtains explicit consent for that Skill")
		Payload(func() {
			Field(1, "origin", String, "Host-assigned source label", func() { MinLength(1) })
			Field(2, "skillUri", String, "Exact activated Skill URI", func() { Format(FormatURI) })
			Field(3, "scriptUri", String, "Exact script URI from the held manifest", func() { Format(FormatURI) })
			Required("origin", "skillUri", "scriptUri")
		})
		Result(func() { Field(1, "outcome", String, "Executed or denied outcome"); Required("outcome") })
	})
	Agent("reader", "Use approved remote instructions without granting remote tool permissions", func() {
		Use("local", func() {
			Tool("execute", "Run a verified script after explicit Skill consent", func() {
				BindTo("execute")
				Confirmation(func() {
					PromptTemplate(`Approve execution for {{ .origin }} {{ .skillUri }}`)
					DeniedResultTemplate(`{"outcome":"Denied"}`)
				})
			})
		})
	})
})
