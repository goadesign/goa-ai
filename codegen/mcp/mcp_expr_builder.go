package codegen

import (
	"fmt"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/expr"

	"goa.design/goa-ai/codegen/shared"
)

// mcpExprBuilder builds Goa expressions for the MCP protocol service.
// It embeds the shared ProtocolExprBuilderBase for common functionality.
type mcpExprBuilder struct {
	*shared.ProtocolExprBuilderBase
	originalService *expr.ServiceExpr
	mcp             *mcpexpr.MCPExpr
	source          *sourceSnapshot
	mcpService      *expr.ServiceExpr
	root            *expr.RootExpr
}

// mcpHTTPServiceConfig carries the resolved JSON-RPC path into the shared HTTP
// service builder. MCP-specific metadata stays in this package; the shared
// builder only owns the transport shape.
type mcpHTTPServiceConfig struct {
	jsonrpcPath string
}

// newMCPExprBuilder creates a new MCP expression builder for the given
// original service and its associated MCP expression configuration.
func newMCPExprBuilder(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, source *sourceSnapshot) *mcpExprBuilder {
	return &mcpExprBuilder{
		ProtocolExprBuilderBase: shared.NewProtocolExprBuilderBase(),
		originalService:         svc,
		mcp:                     mcp,
		source:                  source,
	}
}

func (c mcpHTTPServiceConfig) JSONRPCPath() string {
	return c.jsonrpcPath
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

// buildHTTPService creates the HTTP/JSON-RPC service expression for MCP. The
// builder owns path resolution from the source service, then delegates the
// transport shape to shared.BuildHTTPServiceBase so JSON-RPC route wiring stays
// consistent across protocol generators.
func (b *mcpExprBuilder) buildHTTPService(mcpService *expr.ServiceExpr) *expr.HTTPServiceExpr {
	// Get the JSONRPC path from the stored original configuration
	jsonrpcPath := ""

	if path, ok := b.source.jsonrpcPath(b.originalService.Name); ok && path != "" {
		jsonrpcPath = path
	} else {
		// The shared pure-MCP contract validator should reject this before the
		// builder runs. Keep the local error as a last-resort guard so direct
		// builder use still fails deterministically.
		b.RecordValidationError(fmt.Errorf(missingJSONRPCRouteMessage, b.originalService.Name))
		jsonrpcPath = "/rpc"
	}
	return shared.BuildHTTPServiceBase(mcpService, mcpHTTPServiceConfig{jsonrpcPath: jsonrpcPath})
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
