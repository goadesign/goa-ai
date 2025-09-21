package codegen

import (
	"fmt"

	mcpexpr "goa.design/goa-ai/expr"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
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

// newMCPExprBuilder creates a new MCP expression builder for the given
// original service and its associated MCP expression configuration.
func newMCPExprBuilder(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) *mcpExprBuilder {
	return &mcpExprBuilder{
		originalService: svc,
		mcp:             mcp,
		types:           make(map[string]*expr.UserTypeExpr),
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
	return &expr.AttributeExpr{Type: b.getOrCreateType(name, builder)}
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

// PrepareAndValidate runs Prepare, Validate, and Finalize on the provided root
// without mutating the global Goa expr.Root to keep generation reentrant.
func (b *mcpExprBuilder) PrepareAndValidate(root *expr.RootExpr) error {
	// Temporarily set global expr.Root so Goa validations that reference it
	// resolve services and servers correctly against this temporary root.
	originalRoot := expr.Root
	expr.Root = root
	defer func() { expr.Root = originalRoot }()
	// Step 1: Prepare
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

	// Step 2: Validate
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

	if eval.Context.Errors != nil {
		return eval.Context.Errors
	}

	// Step 3: Finalize
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

// buildHTTPService creates the HTTP/JSON-RPC service expression for MCP,
// configuring routes and SSE for streaming methods.
func (b *mcpExprBuilder) buildHTTPService(mcpService *expr.ServiceExpr) *expr.HTTPServiceExpr {
	// Get the JSONRPC path from the stored original configuration
	jsonrpcPath := ""

	// Use the path that was captured before filtering - required for MCP
	if path, ok := getOriginalJSONRPCPath(b.originalService.Name); ok && path != "" {
		jsonrpcPath = path
	} else {
		// If no path was captured, record a validation error and default to /rpc
		eval.Context.Record(&eval.Error{GoError: fmt.Errorf("service %q must declare JSONRPC(func(){ POST(...) }) with a service-level path", b.originalService.Name)})
		jsonrpcPath = "/rpc"
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
	// Ensure the JSONRPCRoute can compute full paths by giving it an endpoint with Service set
	httpService.JSONRPCRoute.Endpoint = &expr.HTTPEndpointExpr{Service: httpService}

	// Create endpoints for each method
	for _, method := range mcpService.Methods {
		endpoint := &expr.HTTPEndpointExpr{
			MethodExpr: method,
			Service:    httpService,
			Meta: expr.MetaExpr{
				"jsonrpc": []string{},
			},
			// Explicitly set JSON-RPC route so downstream generators (including paths) have it
			Routes: []*expr.RouteExpr{},
		}
		// Ensure JSON-RPC decoders decode params into payload by setting body to method payload
		endpoint.Body = method.Payload
		// Ensure mapped attributes are non-nil for codegen analyze paths
		endpoint.Params = expr.NewEmptyMappedAttributeExpr()
		endpoint.Headers = expr.NewEmptyMappedAttributeExpr()
		endpoint.Cookies = expr.NewEmptyMappedAttributeExpr()
		// Create the route and set its Endpoint back-reference
		rt := &expr.RouteExpr{Method: "POST", Path: jsonrpcPath, Endpoint: endpoint}
		endpoint.Routes = []*expr.RouteExpr{rt}

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

// collectUserTypes returns all user types referenced by the MCP service in a
// deterministic order for stable code generation.
func (b *mcpExprBuilder) collectUserTypes() []expr.UserType {
	// Gather keys and sort to ensure deterministic ordering
	keys := make([]string, 0, len(b.types))
	for k := range b.types {
		keys = append(keys, k)
	}
	// simple insertion sort to avoid extra imports
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	out := make([]expr.UserType, 0, len(keys))
	for _, k := range keys {
		out = append(out, b.types[k])
	}
	return out
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

// ServiceMethodMapping maps MCP operations to original service methods.
type ServiceMethodMapping struct {
	ToolMethods          map[string]string
	ResourceMethods      map[string]string
	DynamicPromptMethods map[string]string
}
