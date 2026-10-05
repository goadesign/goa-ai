// These tests verify that MCP validation uses Goa's inherited URL bindings.
// Resource payloads keep route values for their service call while exposing no
// domain arguments for a fixed resource address.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/internal/mcpinput"
	. "goa.design/goa/v3/dsl"
)

func TestMCPFixedResourceAcceptsInheritedRouteInput(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {
			HTTP(func() { Path("/api") })
		})
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() {
				Path("/organizations/{organization_id}")
				POST("/mcp")
			})
			Method("read", func() {
				Payload(func() {
					Attribute("organization_id", Int64, "Organization selected by the request URL")
					Required("organization_id")
				})
				Result(String)
				Resource("record", "test://records/one", "text/plain")
			})
		})
	})
	method := mcpexpr.Root.MCPServers["records"].Resources[0].Method
	input, err := mcpinput.Arguments(method)
	require.NoError(t, err)
	assert.Nil(t, input.Find("organization_id"))
	assert.NotNil(t, method.Payload.Find("organization_id"))
}

func TestMCPFixedResourceRejectsUnboundDomainInput(t *testing.T) {
	err := runMCPDSLWithError(t, func() {
		API("test", func() {})
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("read", func() {
				Payload(func() {
					Attribute("organization_id", Int64, "Organization supplied as domain input")
					Required("organization_id")
				})
				Result(String)
				Resource("record", "test://records/one", "text/plain")
			})
		})
	})
	require.ErrorContains(t, err, "domain arguments")
}

func TestMCPMissingSharedRouteReportsDesignError(t *testing.T) {
	err := runMCPDSLWithError(t, func() {
		API("test", func() {})
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() {})
			Method("read", func() {
				Result(String)
				Tool("read", "Read a record")
			})
		})
	})
	require.ErrorContains(t, err, "must declare JSONRPC")
}
