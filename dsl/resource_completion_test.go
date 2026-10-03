// These tests verify URI-template completion references with real Goa evaluation.
// Completion names must come from the declared template before generation starts.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	. "goa.design/goa/v3/dsl"
)

func TestMCPResourceCompletion(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		reader := templateReaderDeclaration()
		Service("resources", func() {
			MCP("resources", "1")
			JSONRPC(func() { POST("/resources") })
			reader("read", "test://{id:3}{?fields*}")
			declareResourceCompletion("test://{id:3}{?fields*}", "id")
		})
	})
	assert.Len(t, mcpexpr.Root.MCPServers["resources"].ResourceCompletions, 1)
}

func TestMCPResourceCompletionRejectsUnknownNames(t *testing.T) {
	for _, test := range []struct{ name, template, variable string }{
		{"unknown template", "test://missing/{id}", "id"},
		{"unknown variable", "test://{id:3}{?fields*}", "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() {
				API("test", func() {})
				reader := templateReaderDeclaration()
				Service("resources", func() {
					MCP("resources", "1")
					JSONRPC(func() { POST("/resources") })
					reader("read", "test://{id:3}{?fields*}")
					declareResourceCompletion(test.template, test.variable)
				})
			})
			assert.ErrorContains(t, err, "declared template variable")
		})
	}
}

// declareResourceCompletion uses the established partial-input suggestion shape.
func declareResourceCompletion(template, variable string) {
	Method("suggest", func() {
		Payload(func() {
			Attribute("value", String, "Partial input")
			Attribute("arguments", MapOf(String, String), "Prior variables")
			Required("value")
		})
		Result(func() { Attribute("values", ArrayOf(String), "Suggestions", func() { MaxLength(100) }) })
		ResourceCompletion(template, variable)
	})
}
