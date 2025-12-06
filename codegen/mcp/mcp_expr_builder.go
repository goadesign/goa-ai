package codegen

import (
	"fmt"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"

	"goa.design/goa-ai/codegen/shared"
)

// mcpExprBuilder builds Goa expressions for the MCP protocol service.
// It embeds the shared ProtocolExprBuilderBase for common functionality.
type mcpExprBuilder struct {
	*shared.ProtocolExprBuilderBase
	originalService *expr.ServiceExpr
	mcp             *mcpexpr.MCPExpr
	mcpService      *expr.ServiceExpr
	root            *expr.RootExpr
}

// newMCPExprBuilder creates a new MCP expression builder for the given
// original service and its associated MCP expression configuration.
func newMCPExprBuilder(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) *mcpExprBuilder {
	return &mcpExprBuilder{
		ProtocolExprBuilderBase: shared.NewProtocolExprBuilderBase(),
		originalService:         svc,
		mcp:                     mcp,
	}
}

// BuildServiceExpr creates the Goa service expression that models the MCP
// protocol surface for the original service.
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

// userTypeAttr returns an attribute that references the MCP user type with the
// given name. This ensures downstream codegen treats the payload/result as a
// user type instead of inlining the underlying object, which is important for
// generated client body init functions to return pointer types consistently.
func (b *mcpExprBuilder) userTypeAttr(name string, builder func() *expr.AttributeExpr) *expr.AttributeExpr {
	return b.UserTypeAttr(name, builder)
}

// BuildRootExpr creates a temporary Goa root expression containing only the
// MCP service and its transport setup used to drive code generation.
func (b *mcpExprBuilder) BuildRootExpr(mcpService *expr.ServiceExpr) *expr.RootExpr {
	// Build all MCP types
	b.buildMCPTypes()

	// Create HTTP service for JSON-RPC
	httpService := b.buildHTTPService(mcpService)

	// Create the root
	b.root = &expr.RootExpr{
		Services: []*expr.ServiceExpr{mcpService},
		Types:    b.CollectUserTypes(),
		API: &expr.APIExpr{
			Name:    "MCP",
			Version: "1.0",
			HTTP: &expr.HTTPExpr{
				Services: []*expr.HTTPServiceExpr{httpService},
			},
			JSONRPC: &expr.JSONRPCExpr{
				HTTPExpr: expr.HTTPExpr{
					Services: []*expr.HTTPServiceExpr{httpService},
				},
			},
			GRPC: &expr.GRPCExpr{
				Services: []*expr.GRPCServiceExpr{}, // Initialize empty to avoid nil
			},
			// Add server with service name (not "MCP") for consistent CLI generation
			Servers: []*expr.ServerExpr{
				{
					Name:     mcpService.Name,
					Services: []string{mcpService.Name},
				},
			},
		},
	}

	// Initialize the example generator for the API
	b.root.API.ExampleGenerator = &expr.ExampleGenerator{
		Randomizer: expr.NewFakerRandomizer("MCP"),
	}

	// Set Root reference on HTTP service for proper initialization
	httpService.Root = b.root.API.HTTP

	return b.root
}

// buildHTTPService creates the HTTP/JSON-RPC service expression for MCP,
// configuring routes and SSE for streaming methods.
// Note: MCP has specific path lookup logic that differs from the shared base,
// so we keep this method here rather than using BuildHTTPServiceBase.
func (b *mcpExprBuilder) buildHTTPService(mcpService *expr.ServiceExpr) *expr.HTTPServiceExpr {
	// Get the JSONRPC path from the stored original configuration
	jsonrpcPath := ""

	// Use the path that was captured before filtering - required for MCP
	if path, ok := getOriginalJSONRPCPath(b.originalService.Name); ok && path != "" {
		jsonrpcPath = path
	} else {
		// If no path was captured, record a validation error and default to /rpc
		const missingPathMsg = "service %q must declare JSONRPC(func(){ POST(...) }) with a service-level path"
		eval.Context.Record(&eval.Error{GoError: fmt.Errorf(missingPathMsg, b.originalService.Name)})
		jsonrpcPath = "/rpc"
	}

	httpService := &expr.HTTPServiceExpr{
		ServiceExpr: mcpService,
		JSONRPCRoute: &expr.RouteExpr{
			Method: "POST",
			Path:   jsonrpcPath,
		},
		Paths: []string{},
		SSE:   &expr.HTTPSSEExpr{},
	}
	// Ensure the JSONRPCRoute can compute full paths
	httpService.JSONRPCRoute.Endpoint = &expr.HTTPEndpointExpr{Service: httpService}

	// Create endpoints for each method
	for _, method := range mcpService.Methods {
		endpoint := &expr.HTTPEndpointExpr{
			MethodExpr: method,
			Service:    httpService,
			Meta: expr.MetaExpr{
				"jsonrpc": []string{},
			},
			Routes: []*expr.RouteExpr{},
		}
		endpoint.Body = method.Payload
		endpoint.Params = expr.NewEmptyMappedAttributeExpr()
		endpoint.Headers = expr.NewEmptyMappedAttributeExpr()
		endpoint.Cookies = expr.NewEmptyMappedAttributeExpr()
		rt := &expr.RouteExpr{Method: "POST", Path: jsonrpcPath, Endpoint: endpoint}
		endpoint.Routes = []*expr.RouteExpr{rt}

		// For streaming methods, configure SSE
		if method.Stream == expr.ServerStreamKind {
			endpoint.SSE = &expr.HTTPSSEExpr{}
		}

		httpService.HTTPEndpoints = append(httpService.HTTPEndpoints, endpoint)
	}

	return httpService
}

// BuildServiceMapping creates the mapping between MCP methods and original
// service methods, used by templates to wire adapters and clients.
func (b *mcpExprBuilder) BuildServiceMapping() *ServiceMethodMapping {
	mapping := &ServiceMethodMapping{
		ToolMethods:          make(map[string]string),
		ResourceMethods:      make(map[string]string),
		DynamicPromptMethods: make(map[string]string),
	}

	// Map tools to methods
	for _, tool := range b.mcp.Tools {
		mapping.ToolMethods[tool.Name] = tool.Method.Name
	}

	// Map resources to methods
	for _, resource := range b.mcp.Resources {
		mapping.ResourceMethods[resource.Name] = resource.Method.Name
	}

	// Map dynamic prompts to methods
	if mcpexpr.Root != nil {
		dynamicPrompts := mcpexpr.Root.DynamicPrompts[b.originalService.Name]
		for _, dp := range dynamicPrompts {
			mapping.DynamicPromptMethods[dp.Name] = dp.Method.Name
		}
	}

	return mapping
}

// getOrCreateType retrieves or creates a named user type used by the MCP model.
// This delegates to the embedded base type.
func (b *mcpExprBuilder) getOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
	return b.GetOrCreateType(name, builder)
}

// ServiceMethodMapping maps MCP operations to original service methods.
type ServiceMethodMapping struct {
	ToolMethods          map[string]string
	ResourceMethods      map[string]string
	DynamicPromptMethods map[string]string
}
