// These checks keep server instructions a single authored text owned by one
// MCP server declaration.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPServerInstructions(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("notes", func() {
			MCP("notes", "1", func() { ServerInstructions("Search the notes first.\nThen read them.") })
			JSONRPC(func() { POST("/mcp") })
		})
	})
	assert.Equal(t, "Search the notes first.\nThen read them.", mcpexpr.Root.MCPServers["notes"].Instructions)
}

func TestMCPServerInstructionsRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct {
		name string
		mcp  func()
		want string
	}{
		{name: "empty", mcp: func() { ServerInstructions(" \n") }, want: "ServerInstructions requires a text that is not empty"},
		{
			name: "twice",
			mcp: func() {
				ServerInstructions("Search the notes first.")
				ServerInstructions("Read them.")
			},
			want: "an MCP service can declare only one set of server instructions",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				Service("notes", func() {
					MCP("notes", "1", test.mcp)
					JSONRPC(func() { POST("/mcp") })
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestMCPServerInstructionsRequiresMCPContext(t *testing.T) {
	err := runMCPDSLWithError(t, func() {
		API("test", func() {})
		Service("notes", func() { ServerInstructions("Search the notes first.") })
	})
	assert.Error(t, err)
}
