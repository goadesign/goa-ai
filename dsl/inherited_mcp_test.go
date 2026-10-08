// These authored designs verify resource and completion contracts during normal
// Goa evaluation. Inherited fields and Reference constraints use the same MCP
// validators as directly declared fields.
package dsl_test

import (
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestMCPInheritedResourceAndCompletionContracts(t *testing.T) {
	runMCPDSL(t, func() {
		API("inherited-resources", func() {})
		uriFields := Type("URIFields", func() {
			Field(1, "uri", String, "Exact resource URI", func() { Format(FormatURI) })
			Required("uri")
		})
		input := Type("ReadInput", func() {
			Reference(uriFields)
			Field(1, "uri", String, "Exact resource URI")
		})
		text := Type("ResourceText", func() {
			Extend(uriFields)
			Field(2, "text", String, "Resource contents")
			Required("text")
		})
		itemFields := Type("ItemFields", func() {
			OneOf("content", "Resource contents", func() { Attribute("text", text, "Text contents") })
			Required("content")
		})
		item := Type("ResourceItem", func() { Extend(itemFields) })
		pageFields := Type("PageFields", func() {
			Field(1, "contents", ArrayOfRequired(item), "Resource items")
		})
		page := Type("ResourcePage", func() { Extend(pageFields) })
		suggestionFields := Type("SuggestionFields", func() {
			Field(1, "values", ArrayOf(String), "Suggested URI values", func() { MaxLength(100) })
		})
		suggestions := Type("Suggestions", func() { Extend(suggestionFields) })
		Service("resources", func() {
			MCP("resources", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("read", func() {
				Payload(input)
				Result(page)
				ResourceTemplate("items", "record://items/{id}", "text/plain")
			})
			Method("suggest", func() {
				Payload(func() {
					Field(1, "value", String, "Partial URI variable")
					Field(2, "arguments", MapOf(String, String), "Prior template arguments")
					Required("value")
				})
				Result(suggestions)
				ResourceCompletion("record://items/{id}", "id")
			})
		})
	})
}
