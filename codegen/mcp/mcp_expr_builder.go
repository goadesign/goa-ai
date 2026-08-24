// This file builds the Goa service, types, and JSON-RPC routes that implement
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
		dynamicPrompts  []*mcpexpr.DynamicPromptExpr
		mcpService      *expr.ServiceExpr
	}

	// mcpHTTPServiceConfig gives the shared route builder the JSON-RPC path.
	mcpHTTPServiceConfig struct {
		jsonrpcPath string
	}

	// mcpErrorDefinition describes one error returned by every generated MCP
	// method and the JSON-RPC code sent to clients.
	mcpErrorDefinition struct {
		name        string
		description string
		code        int
	}

	// ServiceMethodMapping names the user service method that handles each MCP request.
	ServiceMethodMapping struct {
		ToolMethods          map[string]string
		ResourceMethods      map[string]string
		DynamicPromptMethods map[string]string
	}
)

// mcpServiceErrors is the complete error contract shared by generated MCP methods.
var mcpServiceErrors = [...]mcpErrorDefinition{
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

// newMCPExprBuilder creates a new MCP expression builder for the given
// original service and its associated MCP expression configuration.
func newMCPExprBuilder(
	svc *expr.ServiceExpr,
	mcp *mcpexpr.MCPExpr,
	dynamicPrompts []*mcpexpr.DynamicPromptExpr,
) *mcpExprBuilder {
	return &mcpExprBuilder{
		ProtocolExprBuilderBase: shared.NewProtocolExprBuilderBase(),
		originalService:         svc,
		mcp:                     mcp,
		dynamicPrompts:          dynamicPrompts,
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
		Errors:      buildMCPServiceErrors(),
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
	root.API.HTTP.Services = removeHTTPServiceByName(root.API.HTTP.Services, b.originalService.Name)
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
	httpService.HTTPErrors = buildMCPHTTPErrorMappings(httpService)
	return httpService
}

// BuildServiceMapping records which user service method handles each MCP method.
func (b *mcpExprBuilder) BuildServiceMapping() *ServiceMethodMapping {
	mapping := &ServiceMethodMapping{
		ToolMethods:          make(map[string]string),
		ResourceMethods:      make(map[string]string),
		DynamicPromptMethods: make(map[string]string),
	}

	for _, tool := range b.mcp.Tools {
		mapping.ToolMethods[tool.Name] = tool.Method.Name
	}

	for _, resource := range b.mcp.Resources {
		mapping.ResourceMethods[resource.Name] = resource.Method.Name
	}

	// Map dynamic prompts to methods.
	for _, prompt := range b.dynamicPrompts {
		mapping.DynamicPromptMethods[prompt.Name] = prompt.Method.Name
	}

	return mapping
}

// getOrCreateType returns the shared named type or creates it on first use.
func (b *mcpExprBuilder) getOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
	return b.GetOrCreateType(name, builder)
}

// buildMCPServiceErrors declares the errors that generated adapter methods may
// return before Goa plans the service code.
func buildMCPServiceErrors() []*expr.ErrorExpr {
	errors := make([]*expr.ErrorExpr, len(mcpServiceErrors))
	for index, definition := range mcpServiceErrors {
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

// buildMCPHTTPErrorMappings assigns each service error its JSON-RPC response
// code before Goa plans the transport code.
func buildMCPHTTPErrorMappings(httpService *expr.HTTPServiceExpr) []*expr.HTTPErrorExpr {
	errors := make([]*expr.HTTPErrorExpr, len(mcpServiceErrors))
	for index, definition := range mcpServiceErrors {
		errors[index] = &expr.HTTPErrorExpr{
			Name: definition.name,
			Response: &expr.HTTPResponseExpr{
				StatusCode:  definition.code,
				Description: definition.description,
				Parent:      httpService,
			},
		}
	}
	return errors
}
