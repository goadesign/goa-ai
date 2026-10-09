// These tests evaluate the public prompt declaration with ordinary Goa payload
// and result types, and check that invalid declarations fail before generation.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPMethodPrompt(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		text := Type("PromptText", func() { Attribute("text", String, "Message text"); Required("text") })
		message := Type("PromptMessage", func() {
			Attribute("role", String, "Message author", func() { Enum("user", "assistant") })
			OneOf("content", "Selected content", func() { Attribute("text", text, "Text message") })
			Required("role", "content")
		})
		Service("prompts", func() {
			MCP("prompts", "1")
			JSONRPC(func() { POST("/prompts") })
			Method("review", func() {
				Payload(func() { Attribute("code", String, "Code to review"); Required("code") })
				Result(func() { Attribute("messages", ArrayOfRequired(message), "Ordered messages") })
				Prompt("review", "Review source code")
				Tool("review-record", "Read the authored message record")
			})
		})
	})
	server := mcpexpr.Root.MCPServers["prompts"]
	require.Len(t, server.MethodPrompts, 1)
	assert.Equal(t, "review", server.MethodPrompts[0].Name)
	assert.Equal(t, server.Tools[0].Method, server.MethodPrompts[0].Method)
}

func TestMCPMethodPromptRejectsInvalidDeclarations(t *testing.T) {
	for _, test := range []struct {
		name    string
		declare func()
		want    string
	}{
		{"service context", func() { Prompt("review", "Review") }, "invalid use"},
		{"primitive payload", func() { Method("review", func() { Payload(String); Prompt("review", "Review") }) }, "payload must be an object"},
		{"numeric argument", func() {
			Method("review", func() { Payload(func() { Attribute("count", Int, "Count") }); Prompt("review", "Review") })
		}, "argument \"count\" must be a string"},
		{"missing messages", func() {
			Method("review", func() { Result(func() { Attribute("text", String, "Text") }); Prompt("review", "Review") })
		}, "messages array"},
		{"duplicate name", func() {
			StaticPrompt("review", "Review", "user", "Review")
			Method("review", func() { Prompt("review", "Review") })
		}, "used more than once"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				Service("prompts", func() { MCP("prompts", "1"); JSONRPC(func() { POST("/prompts") }); test.declare() })
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}
