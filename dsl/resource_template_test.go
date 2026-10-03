// These tests evaluate the public resource-template DSL. Invalid reader shapes
// and competing URI owners must fail before generation can discard information.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPResourceTemplates(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		reader := templateReaderDeclaration()
		Service("resources", func() {
			MCP("resources", "1")
			JSONRPC(func() { POST("/resources") })
			reader("read", "test://items/{id}", "test://items/{id:3}", "test://{+path}{?fields*}")
		})
	})
	assert.Len(t, mcpexpr.Root.MCPServers["resources"].ResourceTemplates, 3)
}

func TestMCPResourceTemplatesRejectInvalidContracts(t *testing.T) {
	for _, test := range []struct {
		name    string
		declare func(func(string, ...string))
		want    string
	}{
		{"service context", func(_ func(string, ...string)) { ResourceTemplate("x", "test://{id}", "text/plain") }, "invalid use"},
		{"invalid syntax", func(reader func(string, ...string)) { reader("read", "test://{id") }, "invalid RFC 6570"},
		{"fixed URI", func(reader func(string, ...string)) { reader("read", "test://fixed") }, "at least one variable"},
		{"duplicate template", func(reader func(string, ...string)) { reader("read", "test://{id}", "test://{id}") }, "used more than once"},
		{"competing readers", func(reader func(string, ...string)) {
			reader("first", "test://{id}")
			reader("second", "test://other/{id}")
		}, "same reader method"},
		{"hidden input", func(_ func(string, ...string)) {
			Method("read", func() {
				Payload(func() { Attribute("uri", String, "URI"); Attribute("id", String, "Guessed value"); Required("uri") })
				Result(String)
				ResourceTemplate("items", "test://{id}", "text/plain")
			})
		}, "only a required uri string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				reader := templateReaderDeclaration()
				Service("resources", func() {
					MCP("resources", "1")
					JSONRPC(func() { POST("/resources") })
					test.declare(reader)
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// templateReaderDeclaration declares shared result types before returning the
// method declaration used by each test's service.
func templateReaderDeclaration() func(string, ...string) {
	text := Type("TemplateTestText", func() {
		Attribute("uri", String, "URI", func() { Format(FormatURI) })
		Attribute("text", String, "Contents")
		Required("uri", "text")
	})
	item := Type("TemplateTestItem", func() {
		OneOf("content", "Selected content", func() { Attribute("text", text, "Text") })
		Required("content")
	})
	return func(name string, templates ...string) {
		Method(name, func() {
			Description("Read resources by exact URI")
			Payload(func() { Attribute("uri", String, "Exact URI"); Required("uri") })
			Result(func() {
				Attribute("contents", ArrayOfRequired(item), "Contents", func() { MinLength(1) })
				Required("contents")
			})
			for _, template := range templates {
				ResourceTemplate(name, template, "text/plain")
			}
		})
	}
}
