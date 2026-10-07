// These tests evaluate an authored Goa method and keep its continuation outside
// tool arguments while retaining the full native service input and outcome.
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

func TestInputExchangeKeepsNativeFieldsOutsideModelArguments(t *testing.T) {
	runMCPDSL(t, func() {
		API("test", func() {})
		Service("records", func() {
			MCP("records", "1")
			JSONRPC(func() { POST("/mcp") })
			Method("read", func() {
				Payload(func() {
					Attribute("record", String, "Record selected by the caller")
					Attribute("continuation", func() {
						Attribute("state", String, "Opaque state owned by this operation")
					})
					Required("record")
				})
				Result(func() {
					OneOf("outcome", func() {
						Attribute("complete", String, "Completed record content")
						Attribute("input_required", func() {
							Attribute("state", String, "State the host echoes for this operation")
						})
					})
					Required("outcome")
				})
				InputExchange("continuation", "outcome")
				Tool("read", "Read one record")
			})
		})
	})
	method := mcpexpr.Root.MCPServers["records"].Tools[0].Method
	mapping, err := mcpinput.InputExchange(method)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	arguments, err := mcpinput.Arguments(method)
	require.NoError(t, err)
	assert.Nil(t, arguments.Find("continuation"))
	assert.NotNil(t, arguments.Find("record"))
	assert.NotNil(t, method.Payload.Find("continuation"))
	assert.NotNil(t, method.Result.Find("outcome"))
	complete, err := mcpinput.CompleteResult(method)
	require.NoError(t, err)
	assert.Same(t, mapping.Complete, complete)
}

// TestBoundInputExchangeRejectsModelContinuation keeps host answers out of Args.
// An explicit domain-only Args remains valid for the same native method.
func TestBoundInputExchangeRejectsModelContinuation(t *testing.T) {
	for _, hostField := range []bool{false, true} {
		name := "domain arguments"
		if hostField {
			name = "host continuation in arguments"
		}
		t.Run(name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				API("test", func() {})
				Service("records", func() {
					Method("read", func() {
						Payload(func() {
							Attribute("record", String, "Selected record")
							Attribute("continuation", func() { Attribute("state", String, "Service-owned state") })
							Required("record")
						})
						Result(func() {
							OneOf("outcome", func() {
								Attribute("complete", String, "Completed record")
								Attribute("input_required", func() { Attribute("state", String, "Returned service state") })
							})
							Required("outcome")
						})
						InputExchange("continuation", "outcome")
					})
					Agent("reader", "Reads records after host input", func() {
						Use("records", func() {
							Tool("read", "Read one record", func() {
								BindTo("read")
								Args(func() {
									Attribute("record", String, "Selected record")
									if hostField {
										Attribute("continuation", func() { Attribute("state", String, "Host state") })
									}
									Required("record")
								})
							})
						})
					})
				})
			})
			if hostField {
				assert.ErrorContains(t, err, `bound input continuation "continuation" belongs to host metadata`)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
