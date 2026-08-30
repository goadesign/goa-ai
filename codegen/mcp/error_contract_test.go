// Package codegen verifies that generated MCP services publish the JSON-RPC errors
// returned by their adapters.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"
)

func TestMCPDispatchMethodsDeclareAndMapProtocolErrors(t *testing.T) {
	svc, methods := testService("assistant", "run", "read")
	mcp := &mcpexpr.MCPExpr{
		Tools: []*mcpexpr.ToolExpr{{Name: "run", Method: methods["run"]}},
		Resources: []*mcpexpr.ResourceExpr{{
			Name:     "read",
			URI:      "doc://read",
			MimeType: "application/json",
			Method:   methods["read"],
		}},
		Prompts: []*mcpexpr.PromptExpr{{
			Name: "help",
			Messages: []*mcpexpr.MessageExpr{{
				Role:    "user",
				Content: "Explain the available methods.",
			}},
		}},
	}
	builder := newMCPExprBuilder(svc, mcp)
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
	require.Empty(t, mcpService.Errors)
	for _, methodName := range []string{"initialize", "notifications/initialized", "ping"} {
		require.Empty(t, mcpService.Method(methodName).Errors, methodName)
	}

	httpService := builder.buildHTTPService(mcpService, "/rpc")
	require.Empty(t, httpService.HTTPErrors)
	for _, methodName := range []string{"tools/list", "resources/list", "prompts/list"} {
		method := mcpService.Method(methodName)
		require.Len(t, method.Errors, 1, methodName)
		require.Equal(t, "invalid_params", method.Errors[0].Name)
		endpoint := httpService.EndpointFor(method)
		require.Len(t, endpoint.HTTPErrors, 1, methodName)
		require.Equal(t, expr.RPCInvalidParams, endpoint.HTTPErrors[0].Response.StatusCode)
	}
	for _, methodName := range []string{"tools/call", "resources/read", "prompts/get"} {
		method := mcpService.Method(methodName)
		require.Len(t, method.Errors, len(want), methodName)
		endpoint := httpService.EndpointFor(method)
		require.Len(t, endpoint.HTTPErrors, len(want), methodName)
		for index, expected := range want {
			methodError := method.Errors[index]
			require.Equal(t, expected.name, methodError.Name)
			require.Equal(t, expected.description, methodError.Description)
			require.True(t, expr.IsErrorResult(methodError.Type))

			transportError := endpoint.HTTPErrors[index]
			require.Equal(t, expected.name, transportError.Name)
			require.Equal(t, expected.description, transportError.Response.Description)
			require.Equal(t, expected.code, transportError.Response.StatusCode)
			require.Same(t, endpoint, transportError.Response.Parent)
		}
	}
}
