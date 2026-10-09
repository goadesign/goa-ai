// These checks author Apps through ordinary tools and resource methods. They
// retain MCP's default caller permissions and reject invalid resource ownership
// before generation can publish a tool catalog.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPAppDeclarations(t *testing.T) {
	runMCPDSL(t, func() {
		API("app", func() {})
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("panel", func() { Result(String); Resource("panel", "ui://records/panel", "text/html;profile=mcp-app") })
			Method("show", func() { Result(String); Tool("show", "Show a record", func() { ToolUI("ui://records/panel") }) })
			Method("refresh", func() { Result(String); Tool("refresh", "Refresh the panel", func() { ToolVisibility("app") }) })
			Method("query", func() { Result(String); Tool("query", "Query records", func() { ToolVisibility("model") }) })
			Method("both", func() {
				Result(String)
				Tool("both", "Use from either caller", func() { ToolVisibility("app", "model") })
			})
		})
	})
	tools := mcpexpr.Root.MCPServers["records"].Tools
	assert.Equal(t, "ui://records/panel", tools[0].UIResourceURI)
	assert.Equal(t, mcpexpr.ModelAndAppVisibility, tools[0].Visibility)
	assert.Equal(t, mcpexpr.AppVisibility, tools[1].Visibility)
	assert.Equal(t, mcpexpr.ModelVisibility, tools[2].Visibility)
	assert.Equal(t, mcpexpr.ModelAndAppVisibility, tools[3].Visibility)
}

func TestMCPInvalidAppDeclarations(t *testing.T) {
	for _, test := range []struct {
		name string
		tool func()
		mime string
		want string
	}{
		{name: "empty resource", tool: func() { ToolUI("") }, want: "nonempty"},
		{name: "network resource", tool: func() { ToolUI("https://example.com/panel") }, want: "ui://"},
		{name: "opaque UI address", tool: func() { ToolUI("ui:panel") }, want: "ui://"},
		{name: "unbound resource", tool: func() { ToolUI("ui://records/missing") }, want: "same MCP service"},
		{name: "wrong HTML MIME", tool: func() { ToolUI("ui://records/panel") }, mime: "text/plain", want: "text/html;profile=mcp-app"},
		{name: "unknown caller", tool: func() { ToolVisibility("browser") }, want: "only model and app"},
		{name: "repeated caller", tool: func() { ToolVisibility("app", "app") }, want: "not repeat"},
		{name: "no caller", tool: func() { ToolVisibility() }, want: "at least one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("app", func() {})
				Service("records", func() {
					MCP("records", "1")
					JSONRPC(func() { POST("/mcp") })
					if test.mime != "" {
						Method("panel", func() { Result(String); Resource("panel", "ui://records/panel", test.mime) })
					}
					Method("show", func() { Result(String); Tool("show", "Show a record", test.tool) })
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestMCPInvalidToolMetadataDeclarations(t *testing.T) {
	for _, test := range []struct {
		name, field string
		result      func()
		want        string
	}{
		{name: "empty", result: func() { Result(String) }, want: "non-empty"},
		{name: "missing", field: "details", result: func() { Result(String) }, want: "must name a field"},
		{name: "raw", field: "details", result: func() { Result(func() { Field(1, "details", Any, "External value") }) }, want: "typed object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("metadata", func() {})
				Service("records", func() {
					MCP("records", "1")
					JSONRPC(func() { POST("/mcp") })
					Method("read", func() { test.result(); Tool("read", "Read a record", func() { ToolMetadata(test.field) }) })
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}
