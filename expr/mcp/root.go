// Package mcp defines the expression types used to represent MCP server
// configuration during Goa design evaluation. These types are populated during
// DSL execution and form the schema used for MCP protocol code generation.
package mcp

import (
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
	// DynamicPrompts maps service names to their dynamic prompt
	// expressions.
	DynamicPrompts map[string][]*DynamicPromptExpr
}

// NewRoot creates a new plugin root expression
func NewRoot() *RootExpr {
	return &RootExpr{
		MCPServers:     make(map[string]*MCPExpr),
		DynamicPrompts: make(map[string][]*DynamicPromptExpr),
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

// WalkSets exposes the nested expressions to the eval engine.
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	var mcps eval.ExpressionSet
	for _, mcp := range r.MCPServers {
		mcps = append(mcps, mcp)
	}
	walk(mcps)

	var caps eval.ExpressionSet
	for _, m := range r.MCPServers {
		if m.Capabilities != nil {
			caps = append(caps, m.Capabilities)
		}
	}
	walk(caps)

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
	walk(resources)

	var prompts eval.ExpressionSet
	var messages eval.ExpressionSet
	for _, m := range r.MCPServers {
		for _, p := range m.Prompts {
			prompts = append(prompts, p)
			for _, msg := range p.Messages {
				messages = append(messages, msg)
			}
		}
	}
	walk(prompts)
	walk(messages)

	var dynPrompts eval.ExpressionSet
	for _, ps := range r.DynamicPrompts {
		for _, p := range ps {
			dynPrompts = append(dynPrompts, p)
		}
	}
	walk(dynPrompts)
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

// GetMCP returns the MCP configuration for a service.
func (r *RootExpr) GetMCP(svc *expr.ServiceExpr) *MCPExpr {
	return r.MCPServers[svc.Name]
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
	_, ok := r.MCPServers[svc.Name]
	return ok
}
