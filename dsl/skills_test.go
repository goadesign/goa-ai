// These checks execute public Skills declarations through Goa's normal DSL
// lifecycle. Invalid discovery contracts fail before any protocol code is emitted.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPSkillDiscoveryBindings(t *testing.T) {
	runMCPDSL(t, func() { skillDiscoveryDesign("") })
	server := mcpexpr.Root.MCPServers["instructions"]
	require.NotNil(t, server.SkillCatalog)
	require.NotNil(t, server.SkillLookup)
	assert.Equal(t, "list", server.SkillCatalog.Name)
	assert.Equal(t, "lookup", server.SkillLookup.Name)
}

func TestMCPSkillDiscoveryRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct{ mode, want string }{
		{"no catalog", "both SkillCatalog and SkillLookup"},
		{"no lookup", "both SkillCatalog and SkillLookup"},
		{"no reader", "requires ResourceReader"},
		{"duplicate catalog", "only one skill catalog"},
		{"duplicate lookup", "only one skill lookup"},
		{"required cursor", "optional cursor"},
		{"extra input", "only a required uri"},
		{"optional skill", "required skill object"},
		{"tagged resources", "untagged OneOf"},
		{"wrong dynamic", "Enum(dynamic)"},
		{"wrong manifest branch", "must be named manifest"},
		{"nullable files", "ArrayOfRequired"},
		{"frontmatter subset", "json.RawMessage"},
		{"invalid digest", "skill file digest"},
		{"invalid size", "Minimum(0)"},
		{"extra entry field", "only required uri"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() { skillDiscoveryDesign(test.mode) })
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// skillDiscoveryDesign declares inherited entries and renamed native fields.
// The file reader is independent of discovery and can serve unlisted skills.
func skillDiscoveryDesign(mode string) {
	API("skill-discovery", func() {})
	file := Type("SkillFile", func() {
		Field(1, "uri", String, "Full file URI", func() { Format(FormatURI) })
		Field(2, "digest", String, "SHA-256 of the served raw bytes", func() {
			if mode != "invalid digest" {
				Pattern(`^sha256:[0-9a-f]{64}$`)
			}
		})
		Field(3, "size", Int64, "Length of the served raw bytes", func() {
			if mode != "invalid size" {
				Minimum(0)
			}
		})
		Required("uri", "digest", "size")
	})
	base := Type("SkillDescription", func() {
		Field(1, "uri", String, "Full SKILL.md URI", func() { Format(FormatURI); Meta("struct:field:name", "Address") })
		if mode == "frontmatter subset" {
			Field(2, "frontmatter", func() { Attribute("name", String, "Incomplete frontmatter") })
		} else {
			Field(2, "frontmatter", Any, "Every authored frontmatter field", func() {
				Meta("struct:field:type", "json.RawMessage", "encoding/json")
			})
		}
		Required("uri", "frontmatter")
	})
	entry := Type("SkillEntry", func() {
		Extend(base)
		OneOf("resources", "Complete file manifest or generated content", func() {
			if mode != "tagged resources" {
				Meta("oneof:json:untagged")
			}
			if mode == "nullable files" {
				Attribute("manifest", ArrayOf(file), "Invalid nullable files")
			} else {
				branch := "manifest"
				if mode == "wrong manifest branch" {
					branch = "files"
				}
				Attribute(branch, ArrayOfRequired(file), "Every file belonging to this skill")
			}
			Attribute("dynamic", String, "Content without stable digests", func() {
				if mode == "wrong dynamic" {
					Enum("other")
				} else {
					Enum("dynamic")
				}
			})
		})
		if mode == "extra entry field" {
			Field(100, "extra", String, "Unsupported entry field")
		}
		Required("resources")
	})
	text := Type("SkillText", func() {
		Field(1, "uri", String, "Full resource URI", func() { Format(FormatURI) })
		Field(2, "text", String, "Requested file contents")
		Required("uri", "text")
	})
	item := Type("SkillContent", func() {
		OneOf("content", "Requested file", func() { Attribute("text", text, "Text contents") })
		Required("content")
	})
	Service("instructions", func() {
		MCP("instructions", "1")
		JSONRPC(func() { POST("/mcp") })
		if mode != "no catalog" {
			Method("list", func() {
				Payload(func() {
					Field(1, "cursor", String, "Opaque catalog cursor")
					if mode == "required cursor" {
						Required("cursor")
					}
				})
				Result(func() {
					Field(1, "skills", ArrayOfRequired(entry), "Complete visible skill entries")
					Field(2, "nextCursor", String, "Next catalog page")
				})
				SkillCatalog()
				if mode == "duplicate catalog" {
					SkillCatalog()
				}
			})
		}
		if mode != "no lookup" {
			Method("lookup", func() {
				Payload(func() {
					Field(1, "uri", String, "Exact skill URI", func() { Meta("struct:field:name", "Address") })
					if mode == "extra input" {
						Field(2, "query", String, "Unmapped domain input")
					}
					Required("uri")
				})
				Result(func() {
					Field(1, "skill", entry, "Complete entry for this known URI")
					if mode != "optional skill" {
						Required("skill")
					}
				})
				SkillLookup()
				if mode == "duplicate lookup" {
					SkillLookup()
				}
			})
		}
		if mode != "no reader" {
			Method("read", func() {
				Payload(func() { Field(1, "uri", String, "Exact resource URI"); Required("uri") })
				Result(func() { Field(1, "contents", ArrayOfRequired(item), "Requested contents") })
				ResourceReader()
			})
		}
	})
}
