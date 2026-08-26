// Package codegen builds the Goa service, types, and JSON-RPC routes that implement
// MCP for one user service.
package codegen

import (
	"fmt"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"

	"goa.design/goa-ai/codegen/shared"
)

type (
	// mcpExprBuilder builds the Goa service that handles MCP requests.
	mcpExprBuilder struct {
		*shared.ProtocolExprBuilderBase
		originalService *expr.ServiceExpr
		mcp             *mcpexpr.MCPExpr
		mcpService      *expr.ServiceExpr
	}

	// mcpHTTPServiceConfig gives the shared route builder the JSON-RPC path.
	mcpHTTPServiceConfig struct {
		jsonrpcPath string
	}

	// mcpErrorDefinition describes one error returned by an MCP method and the
	// JSON-RPC code sent to clients.
	mcpErrorDefinition struct {
		name        string
		description string
		code        int
	}
)

var (
	// mcpInvalidParamsError is returned when request values do not identify a
	// valid operation supported by the generated server.
	mcpInvalidParamsError = mcpErrorDefinition{
		name:        "invalid_params",
		description: "The request parameters do not match the MCP method.",
		code:        expr.RPCInvalidParams,
	}
	// mcpInternalError is returned when a selected operation cannot complete.
	mcpInternalError = mcpErrorDefinition{
		name:        "internal_error",
		description: "The MCP service could not complete the request.",
		code:        expr.RPCInternalError,
	}
	// mcpDispatchErrors are returned by methods that validate a selection and
	// then call authored service code.
	mcpDispatchErrors = [...]mcpErrorDefinition{mcpInvalidParamsError, mcpInternalError}
)

// newMCPExprBuilder creates a new MCP expression builder for the given
// original service and its associated MCP expression configuration.
func newMCPExprBuilder(
	svc *expr.ServiceExpr,
	mcp *mcpexpr.MCPExpr,
) *mcpExprBuilder {
	return &mcpExprBuilder{
		ProtocolExprBuilderBase: shared.NewProtocolExprBuilderBase(),
		originalService:         svc,
		mcp:                     mcp,
	}
}

// JSONRPCPath returns the path used by the generated MCP service.
func (c mcpHTTPServiceConfig) JSONRPCPath() string {
	return c.jsonrpcPath
}

// BuildServiceExpr creates the Goa service that handles MCP for the user service.
func (b *mcpExprBuilder) BuildServiceExpr() *expr.ServiceExpr {
	b.mcpService = &expr.ServiceExpr{
		Name:        "mcp_" + b.originalService.Name,
		Description: fmt.Sprintf("MCP protocol service for %s", b.originalService.Name),
		Methods:     b.buildMethods(),
		Meta: expr.MetaExpr{
			"jsonrpc:service": []string{},
		},
	}

	// Mark all methods as JSON-RPC and set service reference
	for _, m := range b.mcpService.Methods {
		m.Meta = expr.MetaExpr{
			"jsonrpc": []string{},
		}
		m.Service = b.mcpService
	}

	return b.mcpService
}

// userTypeAttr refers to one named MCP type so generated clients use the same
// Go type in method fields and helper functions.
func (b *mcpExprBuilder) userTypeAttr(name string, builder func() *expr.AttributeExpr) *expr.AttributeExpr {
	return b.UserTypeAttr(name, builder)
}

// Attach adds the MCP service, types, and JSON-RPC transport to root. Goa plans
// and writes these expressions with the rest of the design.
func (b *mcpExprBuilder) Attach(root *expr.RootExpr, mcpService *expr.ServiceExpr, jsonrpcPath string) (*expr.HTTPServiceExpr, []expr.UserType) {
	b.buildMCPTypes()
	httpService := b.buildHTTPService(mcpService, jsonrpcPath)
	httpService.Root = &root.API.JSONRPC.HTTPExpr
	root.Services = append(root.Services, mcpService)
	root.API.JSONRPC.Services = replaceHTTPServiceByName(
		root.API.JSONRPC.Services,
		b.originalService.Name,
		httpService,
	)
	return httpService, b.CollectUserTypes()
}

// buildHTTPService gives the generated MCP service the JSON-RPC path declared
// by the user service.
func (b *mcpExprBuilder) buildHTTPService(mcpService *expr.ServiceExpr, jsonrpcPath string) *expr.HTTPServiceExpr {
	httpService := shared.BuildHTTPServiceBase(mcpService, mcpHTTPServiceConfig{jsonrpcPath: jsonrpcPath})
	for _, endpoint := range httpService.HTTPEndpoints {
		if endpoint.MethodExpr.Name == "notifications/initialized" {
			endpoint.JSONRPCNotification = true
		}
		if len(endpoint.MethodExpr.Errors) > 0 {
			endpoint.HTTPErrors = buildMCPHTTPErrorMappings(endpoint)
		}
	}
	return httpService
}

// getOrCreateType returns the shared named type or creates it on first use.
func (b *mcpExprBuilder) getOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
	return b.GetOrCreateType(name, builder)
}

// buildMCPMethodErrors declares the errors returned by one generated adapter
// method before Goa plans the service code.
func buildMCPMethodErrors(definitions ...mcpErrorDefinition) []*expr.ErrorExpr {
	errors := make([]*expr.ErrorExpr, len(definitions))
	for index, definition := range definitions {
		errors[index] = &expr.ErrorExpr{
			Name: definition.name,
			AttributeExpr: &expr.AttributeExpr{
				Type:        expr.ErrorResult,
				Description: definition.description,
			},
		}
	}
	return errors
}

// buildMCPHTTPErrorMappings assigns each method error its JSON-RPC response
// code before Goa plans the transport code.
func buildMCPHTTPErrorMappings(endpoint *expr.HTTPEndpointExpr) []*expr.HTTPErrorExpr {
	errors := make([]*expr.HTTPErrorExpr, 0, len(endpoint.MethodExpr.Errors))
	for _, definition := range mcpDispatchErrors {
		if endpoint.MethodExpr.Error(definition.name) == nil {
			continue
		}
		errors = append(errors, &expr.HTTPErrorExpr{
			Name: definition.name,
			Response: &expr.HTTPResponseExpr{
				StatusCode:  definition.code,
				Description: definition.description,
				Parent:      endpoint,
			},
		})
	}
	return errors
}
