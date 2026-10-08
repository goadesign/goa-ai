// These tests exercise the public resource subscription binding with ordinary
// Goa schemas. Invalid source contracts fail during design evaluation.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPResourceSubscription(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		resourceSubscriptionTypes()
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("read", func() { Result(String); Resource("record", "test://records/one", "text/plain") })
			Method("watch", func() {
				resourceSubscriptionPayload()
				resourceSubscriptionResult()
				SubscriptionSource()
			})
		})
	})
	source := mcpexpr.Root.MCPServers["records"].SubscriptionSource
	require.NotNil(t, source)
	assert.Equal(t, "watch", source.Method.Name)
}

func TestMCPResourceSubscriptionRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct {
		name    string
		declare func()
		want    string
	}{
		{"service context", func() { SubscriptionSource() }, "invalid use"},
		{"unary", func() {
			Method("watch", func() { resourceSubscriptionPayload(); Result(String); SubscriptionSource() })
		}, "only StreamingResult"},
		{"required resources", func() {
			Method("watch", func() {
				Payload(func() {
					Attribute("resources", ArrayOf("RequestedURI"), "Requested resource URIs")
					Required("resources")
				})
				resourceSubscriptionResult()
				SubscriptionSource()
			})
		}, "optional array"},
		{"unexpected input", func() {
			Method("watch", func() {
				Payload(func() { Attribute("query", String, "A query") })
				resourceSubscriptionResult()
				SubscriptionSource()
			})
		}, "unsupported field"},
		{"missing union", func() {
			Method("watch", func() { resourceSubscriptionPayload(); StreamingResult(String); SubscriptionSource() })
		}, "required change OneOf"},
		{"duplicate source", func() {
			Method("watch", func() {
				resourceSubscriptionPayload()
				resourceSubscriptionResult()
				SubscriptionSource()
				SubscriptionSource()
			})
		}, "only one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				resourceSubscriptionTypes()
				Service("records", func() {
					MCP("records", "1")
					JSONRPC(func() { POST("/mcp") })
					Method("read", func() { Result(String); Resource("record", "test://records/one", "text/plain") })
					test.declare()
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// resourceSubscriptionTypes declares shared URI and event types before methods
// reference them. Empty accepted selections remain valid.
func resourceSubscriptionTypes() {
	Type("RequestedURI", String, func() { Format(FormatURI) })
	Type("AcceptedURI", String, func() { Format(FormatURI) })
	Type("Acknowledged", func() { Attribute("resources", ArrayOf("AcceptedURI"), "Authorized resource URIs") })
	Type("Updated", func() {
		Attribute("uri", String, "Changed resource URI", func() { Format(FormatURI) })
		Required("uri")
	})
}

// resourceSubscriptionPayload declares URI selections. URI constraints apply to
// each element rather than the number of updates in the stream.
func resourceSubscriptionPayload() {
	Payload(func() { Attribute("resources", ArrayOf("RequestedURI"), "Requested resource URIs") })
}

// resourceSubscriptionResult declares the acknowledgment and update variants.
// Their runtime order is checked on the originating protocol request.
func resourceSubscriptionResult() {
	StreamingResult(func() {
		OneOf("change", "Resource subscription event", func() {
			Attribute("acknowledged", "Acknowledged", "Authorized resource selection")
			Attribute("updated", "Updated", "Changed resource")
		})
		Required("change")
	})
}
