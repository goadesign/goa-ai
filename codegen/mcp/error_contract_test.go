// Package codegen verifies that generated MCP services publish the JSON-RPC errors
// returned by their adapters.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMCPServiceDeclaresAndMapsProtocolErrors(t *testing.T) {
	svc, _ := testService("assistant", "run")
	builder := newMCPExprBuilder(svc, &mcpexpr.MCPExpr{}, nil)
	mcpService := builder.BuildServiceExpr()

	want := []struct {
		name        string
		description string
		code        int
	}{
		{
			name:        "invalid_params",
			description: "The request parameters do not match the MCP method.",
			code:        expr.RPCInvalidParams,
		},
		{
			name:        "internal_error",
			description: "The MCP service could not complete the request.",
			code:        expr.RPCInternalError,
		},
	}
	require.Len(t, mcpService.Errors, len(want))
	for index, expected := range want {
		actual := mcpService.Errors[index]
		require.Equal(t, expected.name, actual.Name)
		require.Equal(t, expected.description, actual.Description)
		require.True(t, expr.IsErrorResult(actual.Type))
	}

	httpService := builder.buildHTTPService(mcpService, "/rpc")
	require.Len(t, httpService.HTTPErrors, len(want))
	for index, expected := range want {
		actual := httpService.HTTPErrors[index]
		require.Equal(t, expected.name, actual.Name)
		require.Equal(t, expected.description, actual.Response.Description)
		require.Equal(t, expected.code, actual.Response.StatusCode)
		require.Same(t, httpService, actual.Response.Parent)
	}
}
