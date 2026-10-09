// Package mcp defines the expression types used to represent MCP server
// configuration during Goa design evaluation. These types are populated during
// DSL execution and form the schema used for MCP protocol code generation.
package mcp

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// Root is the plugin root instance holding all MCP server configurations.
var Root *RootExpr

func init() {
	Root = NewRoot()
	if err := eval.Register(Root); err != nil {
		panic(err)
	}
}

// RootExpr is the top-level root expression for all MCP server declarations.
type RootExpr struct {
	// MCPServers maps service names to their MCP server configurations.
	MCPServers map[string]*MCPExpr
}

// NewRoot creates a new plugin root expression
func NewRoot() *RootExpr {
	return &RootExpr{
		MCPServers: make(map[string]*MCPExpr),
	}
}

// EvalName returns the plugin name.
func (r *RootExpr) EvalName() string {
	return "MCP plugin"
}

// DependsOn returns the list of other roots this plugin depends on.
func (r *RootExpr) DependsOn() []eval.Root {
	return []eval.Root{expr.Root}
}

// Packages returns the DSL packages that should be recognized for error
// reporting.
func (r *RootExpr) Packages() []string {
	return []string{"goa.design/goa-ai/dsl"}
}

// Prepare records path fields after Goa has prepared each authored endpoint.
// Subsequent MCP validation sees domain arguments without URL-owned values.
func (r *RootExpr) Prepare() {
	for _, service := range expr.Root.API.JSONRPC.Services {
		mcp := r.GetMCP(service.ServiceExpr)
		if mcp == nil {
			continue
		}
		mcpinput.BindTransport(service)
	}
}

// WalkSets exposes the nested expressions to the eval engine.
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	mcps := make(eval.ExpressionSet, 0, len(r.MCPServers))
	for _, mcp := range r.MCPServers {
		mcps = append(mcps, mcp)
	}
	walk(mcps)

	var tools eval.ExpressionSet
	for _, m := range r.MCPServers {
		for _, t := range m.Tools {
			tools = append(tools, t)
		}
	}
	walk(tools)

	var resources eval.ExpressionSet
	for _, m := range r.MCPServers {
		for _, rsrc := range m.Resources {
			resources = append(resources, rsrc)
		}
	}
	for _, server := range r.MCPServers {
		for _, template := range server.ResourceTemplates {
			resources = append(resources, template)
		}
	}
	walk(resources)

	var prompts eval.ExpressionSet
	var messages eval.ExpressionSet
	for _, m := range r.MCPServers {
		for _, p := range m.MethodPrompts {
			prompts = append(prompts, p)
		}
		for _, p := range m.Prompts {
			prompts = append(prompts, p)
			for _, msg := range p.Messages {
				messages = append(messages, msg)
			}
		}
	}
	walk(prompts)
	walk(messages)
	var completions eval.ExpressionSet
	for _, server := range r.MCPServers {
		for _, completion := range server.PromptCompletions {
			completions = append(completions, completion)
		}
	}
	for _, server := range r.MCPServers {
		for _, completion := range server.ResourceCompletions {
			completions = append(completions, completion)
		}
	}
	walk(completions)
	var subscriptions eval.ExpressionSet
	for _, server := range r.MCPServers {
		if server.SubscriptionSource != nil {
			subscriptions = append(subscriptions, server.SubscriptionSource)
		}
	}
	walk(subscriptions)
}

// RegisterMCP registers an MCP server configuration for a service
func (r *RootExpr) RegisterMCP(svc *expr.ServiceExpr, mcp *MCPExpr) {
	mcp.Service = svc
	r.MCPServers[svc.Name] = mcp
}

// GetMCP returns the MCP configuration for a service.
func (r *RootExpr) GetMCP(svc *expr.ServiceExpr) *MCPExpr {
	mcp := r.MCPServers[svc.Name]
	if mcp == nil || mcp.Service != svc {
		return nil
	}
	return mcp
}

// ServiceMCP returns the MCP configuration for a service name and optional
// toolset (server name) filter. When toolset is empty, it returns the MCP
// server for the service if present.
func (r *RootExpr) ServiceMCP(service, toolset string) *MCPExpr {
	m, ok := r.MCPServers[service]
	if !ok {
		return nil
	}
	if toolset != "" && m.Name != toolset {
		return nil
	}
	return m
}

// HasMCP returns true if the service has an MCP configuration.
func (r *RootExpr) HasMCP(svc *expr.ServiceExpr) bool {
	return r.GetMCP(svc) != nil
}
