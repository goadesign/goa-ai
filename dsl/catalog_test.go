// These checks exercise authored tool and prompt catalogs during design
// evaluation. Catalogs select declared operations; malformed page contracts fail
// before a generated server could advertise or invoke the catalog method.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPCatalogMethods(t *testing.T) {
	runMCPDSL(t, func() {
		API("catalogs", func() {})
		Type("CatalogName", String)
		Type("PageCursor", String)
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("read", func() {
				Result(String)
				Tool("read", "Read a record")
			})
			StaticPrompt("review", "Review a record", "user", "Review this record")
			Method("list_tools", func() { catalogPage("tools"); ToolCatalog() })
			Method("list_prompts", func() { catalogPage("prompts"); PromptCatalog() })
		})
	})
	server := mcpexpr.Root.MCPServers["records"]
	require.NotNil(t, server.ToolCatalog)
	require.NotNil(t, server.PromptCatalog)
	assert.Equal(t, "list_tools", server.ToolCatalog.Name)
	assert.Equal(t, "list_prompts", server.PromptCatalog.Name)
}

func TestMCPCatalogRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct {
		name    string
		declare func()
		want    string
	}{
		{"service placement", ToolCatalog, "invalid use"},
		{"duplicate tool source", func() {
			Method("list", func() { catalogPage("tools"); ToolCatalog(); ToolCatalog() })
		}, "only one tool catalog"},
		{"duplicate prompt source", func() {
			Method("list", func() { catalogPage("prompts"); PromptCatalog(); PromptCatalog() })
		}, "only one prompt catalog"},
		{"required cursor", func() {
			Method("list", func() {
				Payload(func() { Attribute("cursor", String, "Page cursor"); Required("cursor") })
				Result(func() { Attribute("tools", ArrayOf(String), "Visible names") })
				ToolCatalog()
			})
		}, "optional cursor"},
		{"extra domain input", func() {
			Method("list", func() {
				Payload(func() { Attribute("cursor", String, "Page cursor"); Attribute("query", String, "Search text") })
				Result(func() { Attribute("tools", ArrayOf(String), "Visible names") })
				ToolCatalog()
			})
		}, "only an optional cursor"},
		{"runtime tool definitions", func() {
			Method("list", func() {
				Payload(func() { Attribute("cursor", String, "Page cursor") })
				Result(func() { Attribute("tools", ArrayOf(Int), "Invalid operation definitions") })
				ToolCatalog()
			})
		}, "array of declared names"},
		{"required next cursor", func() {
			Method("list", func() {
				Payload(func() { Attribute("cursor", String, "Page cursor") })
				Result(func() {
					Attribute("prompts", ArrayOf(String), "Visible names")
					Attribute("nextCursor", String, "Next page")
					Required("nextCursor")
				})
				PromptCatalog()
			})
		}, "nextCursor must be an optional string"},
		{"unmapped output", func() {
			Method("list", func() {
				Payload(func() { Attribute("cursor", String, "Page cursor") })
				Result(func() {
					Attribute("tools", ArrayOf(String), "Visible names")
					Attribute("other", String, "Unmapped value")
				})
				ToolCatalog()
			})
		}, "unsupported field"},
		{"streaming catalog", func() {
			Method("list", func() {
				Payload(func() { Attribute("cursor", String, "Page cursor") })
				StreamingResult(String)
				ToolCatalog()
			})
		}, "must be unary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("catalogs", func() {})
				Service("records", func() {
					MCP("records", "1")
					JSONRPC(func() { POST("/mcp") })
					Method("read", func() { Result(String); Tool("read", "Read a record") })
					StaticPrompt("review", "Review a record", "user", "Review a record")
					test.declare()
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// catalogPage declares names and cursors with authored aliases. Empty pages
// remain valid, including the final page whose nextCursor is absent.
func catalogPage(collection string) {
	Payload(func() { Attribute("cursor", "PageCursor", "Opaque cursor from a previous page") })
	Result(func() {
		Attribute(collection, ArrayOf("CatalogName"), "Visible declared operation names")
		Attribute("nextCursor", "PageCursor", "Opaque cursor for another page")
	})
}

func TestMCPCatalogSubscriptionRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct{ name, mode, want string }{
		{"fixed catalog", "fixed", "requires an authored changing catalog"},
		{"required selection", "required", "optional boolean"},
		{"string selection", "string", "optional boolean"},
		{"missing acceptance", "acceptance", "acknowledged selections must match"},
		{"missing change", "missing", "must declare \"tools_changed\""},
		{"unexpected event data", "data", "empty object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("catalog-source", func() {})
				Service("records", func() {
					MCP("records", "1")
					JSONRPC(func() { POST("/mcp") })
					Method("read", func() { Result(String); Tool("read", "Read a record") })
					if test.mode != "fixed" {
						Method("list", func() {
							Payload(func() { Attribute("cursor", String, "Page cursor") })
							Result(func() { Attribute("tools", ArrayOf(String), "Visible declared names") })
							ToolCatalog()
						})
					}
					Method("watch", func() {
						Payload(func() {
							if test.mode == "string" {
								Attribute("toolsListChanged", String, "Invalid selection")
							} else {
								Attribute("toolsListChanged", Boolean, "Select tool changes")
							}
							if test.mode == "required" {
								Required("toolsListChanged")
							}
						})
						StreamingResult(func() {
							OneOf("change", "A catalog selection or change", func() {
								Attribute("acknowledged", func() {
									Description("Authorized selections")
									if test.mode != "acceptance" {
										Attribute("toolsListChanged", Boolean, "Accepted tool changes")
									}
								})
								if test.mode != "missing" {
									Attribute("tools_changed", func() {
										Description("Tool catalog changed")
										if test.mode == "data" {
											Attribute("name", String, "Unexpected data")
										}
									})
								}
							})
							Required("change")
						})
						SubscriptionSource()
					})
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}
