package expr

import (
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// Root is the plugin root instance
var Root *RootExpr

// RootExpr is the plugin root expression
type RootExpr struct {
	// MCPServers maps service names to their MCP configurations
	MCPServers map[string]*MCPExpr
	// DynamicPrompts maps service names to their dynamic prompts
	DynamicPrompts map[string][]*DynamicPromptExpr
}

// NewRoot creates a new plugin root expression
func NewRoot() *RootExpr {
	return &RootExpr{
		MCPServers:     make(map[string]*MCPExpr),
		DynamicPrompts: make(map[string][]*DynamicPromptExpr),
	}
}

// EvalName returns the plugin name
func (r *RootExpr) EvalName() string {
	return "MCP plugin"
}

// DependsOn returns the list of other roots this plugin depends on
func (r *RootExpr) DependsOn() []eval.Root {
	return []eval.Root{expr.Root}
}

// Packages returns the DSL packages that should be recognized for error reporting
func (r *RootExpr) Packages() []string {
	return []string{
		"goa.design/goa-ai/dsl",
	}
}

// WalkSets returns the expression tree walk sets
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	// Walk MCP servers
	var mcps eval.ExpressionSet
	for _, mcp := range r.MCPServers {
		mcps = append(mcps, mcp)
	}
	if len(mcps) > 0 {
		walk(mcps)
	}

	// Walk capabilities
	var caps eval.ExpressionSet
	for _, mcp := range r.MCPServers {
		if mcp.Capabilities != nil {
			caps = append(caps, mcp.Capabilities)
		}
	}
	if len(caps) > 0 {
		walk(caps)
	}

	// Walk tools
	var tools eval.ExpressionSet
	for _, mcp := range r.MCPServers {
		for _, tool := range mcp.Tools {
			tools = append(tools, tool)
		}
	}
	if len(tools) > 0 {
		walk(tools)
	}

	// Walk resources
	var resources eval.ExpressionSet
	for _, mcp := range r.MCPServers {
		for _, resource := range mcp.Resources {
			resources = append(resources, resource)
		}
	}
	if len(resources) > 0 {
		walk(resources)
	}

	// Walk prompts and messages
	var prompts eval.ExpressionSet
	var messages eval.ExpressionSet
	for _, mcp := range r.MCPServers {
		for _, prompt := range mcp.Prompts {
			prompts = append(prompts, prompt)
			for _, msg := range prompt.Messages {
				messages = append(messages, msg)
			}
		}
	}
	if len(prompts) > 0 {
		walk(prompts)
	}
	if len(messages) > 0 {
		walk(messages)
	}

	// Walk dynamic prompts
	var dynPrompts eval.ExpressionSet
	for _, prompts := range r.DynamicPrompts {
		for _, prompt := range prompts {
			dynPrompts = append(dynPrompts, prompt)
		}
	}
	if len(dynPrompts) > 0 {
		walk(dynPrompts)
	}
}

// RegisterMCP registers an MCP server configuration for a service
func (r *RootExpr) RegisterMCP(svc *expr.ServiceExpr, mcp *MCPExpr) {
	mcp.Service = svc
	r.MCPServers[svc.Name] = mcp
}

// RegisterDynamicPrompt registers a dynamic prompt for a service
func (r *RootExpr) RegisterDynamicPrompt(svc *expr.ServiceExpr, prompt *DynamicPromptExpr) {
	r.DynamicPrompts[svc.Name] = append(r.DynamicPrompts[svc.Name], prompt)
}

// GetMCP returns the MCP configuration for a service
func (r *RootExpr) GetMCP(svc *expr.ServiceExpr) *MCPExpr {
	return r.MCPServers[svc.Name]
}

// HasMCP returns true if the service has an MCP configuration
func (r *RootExpr) HasMCP(svc *expr.ServiceExpr) bool {
	_, ok := r.MCPServers[svc.Name]
	return ok
}
