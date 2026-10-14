// These checks keep runtime tool ownership explicit in the public MCP design.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPDynamicTools(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("catalog", func() {
			MCP("catalog", "1", func() { DynamicTools() })
			JSONRPC(func() { POST("/mcp") })
		})
	})
	assert.True(t, mcpexpr.Root.MCPServers["catalog"].DynamicTools)
	assert.Empty(t, mcpexpr.Root.MCPServers["catalog"].Tools)
}

func TestMCPDynamicToolsRejectsConflictingOwners(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		name := "tool"
		if catalog {
			name = "catalog"
		}
		t.Run(name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				Service("catalog", func() {
					MCP("catalog", "1", func() { DynamicTools() })
					JSONRPC(func() { POST("/mcp") })
					Method("list", func() {
						if catalog {
							Payload(func() { Attribute("cursor", String) })
							Result(func() { Attribute("tools", ArrayOf(String)); Attribute("nextCursor", String) })
							ToolCatalog()
						} else {
							Result(String)
							Tool("echo", "Echo text")
						}
					})
				})
			})
			assert.ErrorContains(t, err, "DynamicTools cannot be combined with Tool or ToolCatalog")
		})
	}
}

func TestMCPDynamicToolsRequiresMCPContext(t *testing.T) {
	err := runMCPDSLWithError(t, func() { API("test", func() {}); Service("catalog", func() { DynamicTools() }) })
	assert.Error(t, err)
}
