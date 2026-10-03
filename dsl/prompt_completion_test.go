// These tests check the public completion binding with real Goa evaluation.
// Invalid bindings must fail before the generator can discard input or output.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPPromptCompletion(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		declareTarget := completionTargetDeclaration()
		Service("prompts", func() {
			MCP("prompts", "1")
			JSONRPC(func() { POST("/prompts") })
			declareTarget()
			Method("suggest", func() {
				Payload(func() {
					Attribute("value", String, "Partial input")
					Attribute("arguments", MapOf(String, String), "Prior arguments")
					Required("value")
				})
				Result(func() {
					Attribute("values", ArrayOf(String), "Ranked suggestions", func() { MaxLength(100) })
					Attribute("total", Int64, "All matches")
					Attribute("hasMore", Boolean, "More matches exist")
				})
				PromptCompletion("review", "style")
			})
		})
	})
	server := mcpexpr.Root.MCPServers["prompts"]
	assert.Len(t, server.PromptCompletions, 1)
	assert.Equal(t, "style", server.PromptCompletions[0].Argument)
}

func TestMCPPromptCompletionRejectsInvalidContracts(t *testing.T) {
	for _, test := range []struct {
		name    string
		declare func()
		want    string
	}{
		{"service context", func() { PromptCompletion("review", "style") }, "invalid use"},
		{"missing context field", func() {
			Method("suggest", func() {
				Payload(func() { Attribute("value", String, "Input"); Required("value") })
				Result(func() { Attribute("values", ArrayOf(String), "Suggestions", func() { MaxLength(100) }) })
				PromptCompletion("review", "style")
			})
		}, "must declare optional arguments"},
		{"unknown prompt", func() { declareValidCompletion("missing", "style") }, "must select a declared prompt argument"},
		{"unknown argument", func() { declareValidCompletion("review", "missing") }, "must select a declared prompt argument"},
		{"duplicate binding", func() {
			declareValidCompletion("review", "style")
			Method("again", func() {
				Payload(func() {
					Attribute("value", String, "Input")
					Attribute("arguments", MapOf(String, String), "Context")
					Required("value")
				})
				Result(func() { Attribute("values", ArrayOf(String), "Suggestions", func() { MaxLength(100) }) })
				PromptCompletion("review", "style")
			})
		}, "used more than once"},
		{"missing limit", func() {
			Method("suggest", func() {
				Payload(func() {
					Attribute("value", String, "Input")
					Attribute("arguments", MapOf(String, String), "Context")
					Required("value")
				})
				Result(func() { Attribute("values", ArrayOf(String), "Suggestions") })
				PromptCompletion("review", "style")
			})
		}, "MaxLength(100)"},
		{"oversized limit", func() {
			Method("suggest", func() {
				Payload(func() {
					Attribute("value", String, "Input")
					Attribute("arguments", MapOf(String, String), "Context")
					Required("value")
				})
				Result(func() { Attribute("values", ArrayOf(String), "Suggestions", func() { MaxLength(101) }) })
				PromptCompletion("review", "style")
			})
		}, "MaxLength(100)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				declareTarget := completionTargetDeclaration()
				Service("prompts", func() {
					MCP("prompts", "1")
					JSONRPC(func() { POST("/prompts") })
					declareTarget()
					test.declare()
				})
			})
			assert.ErrorContains(t, err, test.want)
		})
	}
}

// completionTargetDeclaration defines shared types at API level and returns
// the method declaration that a service uses as the completion target.
func completionTargetDeclaration() func() {
	text := Type("SuggestionPromptText", func() { Attribute("text", String, "Message"); Required("text") })
	message := Type("SuggestionPromptMessage", func() {
		Attribute("role", String, "Author", func() { Enum("user") })
		OneOf("content", "Selected content", func() { Attribute("text", text, "Text") })
		Required("role", "content")
	})
	return func() {
		Method("review", func() {
			Payload(func() { Attribute("style", String, "Review style") })
			Result(func() { Attribute("messages", ArrayOfRequired(message), "Messages") })
			Prompt("review", "Review")
		})
	}
}

// declareValidCompletion keeps negative binding tests focused on name selection.
func declareValidCompletion(prompt, argument string) {
	Method("suggest", func() {
		Payload(func() {
			Attribute("value", String, "Input")
			Attribute("arguments", MapOf(String, String), "Context")
			Required("value")
		})
		Result(func() { Attribute("values", ArrayOf(String), "Suggestions", func() { MaxLength(100) }) })
		PromptCompletion(prompt, argument)
	})
}
