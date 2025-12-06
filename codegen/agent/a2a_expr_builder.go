package codegen

import (
	"fmt"

	"goa.design/goa-ai/codegen/shared"
	"goa.design/goa/v3/expr"
)

type (
	// a2aExprBuilder builds Goa expressions for the A2A protocol service.
	// It embeds the shared ProtocolExprBuilderBase for common functionality.
	a2aExprBuilder struct {
		*shared.ProtocolExprBuilderBase
		agent      *AgentData
		a2aService *expr.ServiceExpr
		root       *expr.RootExpr
	}

	// a2aConfig implements shared.ProtocolConfig for A2A.
	a2aConfig struct {
		path    string
		version string
	}
)

// newA2AExprBuilder creates a new A2A expression builder for the given agent.
func newA2AExprBuilder(agent *AgentData) *a2aExprBuilder {
	return &a2aExprBuilder{
		ProtocolExprBuilderBase: shared.NewProtocolExprBuilderBase(),
		agent:                   agent,
	}
}

// a2aConfig methods implement shared.ProtocolConfig

func (c *a2aConfig) JSONRPCPath() string          { return c.path }
func (c *a2aConfig) ProtocolVersion() string      { return c.version }
func (c *a2aConfig) Capabilities() map[string]any { return map[string]any{"streaming": true} }
func (c *a2aConfig) Name() string                 { return "A2A" }

// BuildServiceExpr creates the Goa service expression that models the A2A
// protocol surface for the agent.
func (b *a2aExprBuilder) BuildServiceExpr() *expr.ServiceExpr {
	b.a2aService = &expr.ServiceExpr{
		Name:        "a2a_" + b.agent.Name,
		Description: fmt.Sprintf("A2A protocol service for %s agent", b.agent.Name),
		Methods:     b.buildMethods(),
		Meta: expr.MetaExpr{
			"jsonrpc:service": []string{},
		},
	}

	// Mark all methods as JSON-RPC and set service reference
	for _, m := range b.a2aService.Methods {
		m.Meta = expr.MetaExpr{
			"jsonrpc": []string{},
		}
		m.Service = b.a2aService
	}

	return b.a2aService
}

// userTypeAttr returns an attribute that references the A2A user type with the
// given name. This ensures downstream codegen treats the payload/result as a
// user type instead of inlining the underlying object.
func (b *a2aExprBuilder) userTypeAttr(name string, builder func() *expr.AttributeExpr) *expr.AttributeExpr {
	return b.UserTypeAttr(name, builder)
}

// BuildRootExpr creates a temporary Goa root expression containing only the
// A2A service and its transport setup used to drive code generation.
func (b *a2aExprBuilder) BuildRootExpr(a2aService *expr.ServiceExpr) *expr.RootExpr {
	// Build all A2A types
	b.buildA2ATypes()

	// Create HTTP service for JSON-RPC using shared helper
	config := &a2aConfig{path: "/a2a", version: "1.0"}
	httpService := shared.BuildHTTPServiceBase(a2aService, config)

	// Create the root
	b.root = &expr.RootExpr{
		Services: []*expr.ServiceExpr{a2aService},
		Types:    b.CollectUserTypes(),
		API: &expr.APIExpr{
			Name:    "A2A",
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
			Servers: []*expr.ServerExpr{
				{
					Name:     a2aService.Name,
					Services: []string{a2aService.Name},
				},
			},
		},
	}

	// Initialize the example generator for the API
	b.root.API.ExampleGenerator = &expr.ExampleGenerator{
		Randomizer: expr.NewFakerRandomizer("A2A"),
	}

	// Set Root reference on HTTP service for proper initialization
	httpService.Root = b.root.API.HTTP

	return b.root
}

// getOrCreateType retrieves or creates a named user type used by the A2A model.
// This delegates to the embedded base type.
func (b *a2aExprBuilder) getOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
	return b.GetOrCreateType(name, builder)
}
