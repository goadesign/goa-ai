package codegen

import (
	"fmt"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

type (
	// mcpExprBuilder builds Goa expressions for the MCP protocol service
	mcpExprBuilder struct {
		originalService *expr.ServiceExpr
		mcp             *mcpexpr.MCPExpr
		mcpService      *expr.ServiceExpr
		root            *expr.RootExpr
		types           map[string]*expr.UserTypeExpr
	}
)

// newMCPExprBuilder creates a new MCP expression builder
func newMCPExprBuilder(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) *mcpExprBuilder {
	return &mcpExprBuilder{
		originalService: svc,
		mcp:             mcp,
		types:           make(map[string]*expr.UserTypeExpr),
	}
}

// BuildServiceExpr creates the MCP service expression
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

// BuildRootExpr creates a temporary root expression for MCP generation
func (b *mcpExprBuilder) BuildRootExpr(mcpService *expr.ServiceExpr) *expr.RootExpr {
	// Build all MCP types
	b.buildMCPTypes()

	// Create HTTP service for JSON-RPC
	httpService := b.buildHTTPService(mcpService)

	// Create the root
	b.root = &expr.RootExpr{
		Services: []*expr.ServiceExpr{mcpService},
		Types:    b.collectUserTypes(),
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

// PrepareAndValidate prepares, validates, and finalizes the MCP expressions
func (b *mcpExprBuilder) PrepareAndValidate(root *expr.RootExpr) error {
	// Save the original global Root and temporarily set our root
	// This is needed because Goa's Prepare/Finalize methods reference the global Root
	originalRoot := expr.Root
	expr.Root = root
	defer func() {
		// Restore original root
		expr.Root = originalRoot
	}()

	// Use eval engine to process expressions in the correct order
	// This mimics what RunDSL does but without the DSL execution phase

	// Step 1: Prepare phase - walk the expression tree and call Prepare
	prepareSet := func(set eval.ExpressionSet) {
		for _, def := range set {
			if def == nil {
				continue
			}
			if p, ok := def.(eval.Preparer); ok {
				p.Prepare()
			}
		}
	}
	prepareSet(eval.ExpressionSet{root})
	root.WalkSets(prepareSet)

	// Step 2: Validate phase - walk the expression tree and call Validate
	validateSet := func(set eval.ExpressionSet) {
		errors := &eval.ValidationErrors{}
		for _, def := range set {
			if def == nil {
				continue
			}
			if validate, ok := def.(eval.Validator); ok {
				if err := validate.Validate(); err != nil {
					errors.AddError(def, err)
				}
			}
		}
		if len(errors.Errors) > 0 {
			eval.Context.Record(&eval.Error{GoError: errors})
		}
	}
	validateSet(eval.ExpressionSet{root})
	root.WalkSets(validateSet)

	// Check for validation errors
	if eval.Context.Errors != nil {
		return eval.Context.Errors
	}

	// Step 3: Finalize phase - walk the expression tree and call Finalize
	finalizeSet := func(set eval.ExpressionSet) {
		for _, def := range set {
			if def == nil {
				continue
			}
			if f, ok := def.(eval.Finalizer); ok {
				f.Finalize()
			}
		}
	}
	finalizeSet(eval.ExpressionSet{root})
	root.WalkSets(finalizeSet)

	return nil
}

// buildHTTPService creates the HTTP service for JSON-RPC transport
func (b *mcpExprBuilder) buildHTTPService(mcpService *expr.ServiceExpr) *expr.HTTPServiceExpr {
	// Get the JSONRPC path from the stored original configuration
	jsonrpcPath := ""

	// Use the path that was captured before filtering - required for MCP
	if path, ok := originalJSONRPCPaths[b.originalService.Name]; ok && path != "" {
		jsonrpcPath = path
	} else {
		// If no path was captured, the design doesn't have JSONRPC configured properly
		panic(fmt.Sprintf("MCP service %s requires JSONRPC transport with a path defined", b.originalService.Name))
	}

	httpService := &expr.HTTPServiceExpr{
		ServiceExpr: mcpService,
		// For JSON-RPC, set the service-level JSONRPCRoute
		JSONRPCRoute: &expr.RouteExpr{
			Method: "POST",
			Path:   jsonrpcPath,
		},
		// No need to set Paths for JSON-RPC services
		Paths: []string{},
		// Enable SSE for streaming endpoints
		SSE: &expr.HTTPSSEExpr{},
	}

	// Create endpoints for each method
	for _, method := range mcpService.Methods {
		endpoint := &expr.HTTPEndpointExpr{
			MethodExpr: method,
			Service:    httpService,
			Meta: expr.MetaExpr{
				"jsonrpc": []string{},
			},
			// Explicitly set JSON-RPC route so downstream generators (including paths) have it
			Routes: []*expr.RouteExpr{{
				Method: "POST",
				Path:   jsonrpcPath,
			}},
		}

		// For streaming methods, configure SSE
		if method.Stream == expr.ServerStreamKind {
			endpoint.SSE = &expr.HTTPSSEExpr{}
		}

		// Prepare and Finalize will initialize all required fields
		httpService.HTTPEndpoints = append(httpService.HTTPEndpoints, endpoint)
	}

	// Root will be set when BuildRootExpr is called

	return httpService
}

// collectUserTypes returns all defined types as UserType slice
func (b *mcpExprBuilder) collectUserTypes() []expr.UserType {
	types := make([]expr.UserType, 0, len(b.types))
	for _, t := range b.types {
		types = append(types, t)
	}
	return types
}

// BuildServiceMapping creates the mapping between MCP methods and original service methods
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

// getOrCreateType retrieves or creates a user type
func (b *mcpExprBuilder) getOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
	if t, ok := b.types[name]; ok {
		return t
	}

	t := &expr.UserTypeExpr{
		TypeName:      name,
		AttributeExpr: builder(),
	}
	b.types[name] = t
	return t
}

// ServiceMethodMapping maps MCP operations to original service methods
type ServiceMethodMapping struct {
	ToolMethods          map[string]string
	ResourceMethods      map[string]string
	DynamicPromptMethods map[string]string
}
