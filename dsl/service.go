package dsl

import (
	_ "goa.design/goa-ai/codegen/mcp" // Registers the MCP codegen plugin with Goa
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// MCPServer enables Model Context Protocol (MCP) for the current service and sets server metadata.
func MCPServer(name, version string, opts ...func(*exprmcp.MCPExpr)) {
	svc, ok := eval.Current().(*goaexpr.ServiceExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	m := &exprmcp.MCPExpr{Service: svc, Name: name, Version: version, Description: svc.Description, Capabilities: &exprmcp.CapabilitiesExpr{}}
	for _, o := range opts {
		if o != nil {
			o(m)
		}
	}
	if r := exprmcp.Root; r != nil {
		r.RegisterMCP(svc, m)
	}
}

// ProtocolVersion sets the MCP protocol version (e.g., "2025-06-18").
func ProtocolVersion(version string) func(*exprmcp.MCPExpr) {
	return func(m *exprmcp.MCPExpr) { m.ProtocolVersion = version }
}

// Tool marks the current method as an MCP tool.
// Call Tool inside a Method DSL to expose it to MCP clients. The method payload
// is recorded as the tool input schema and the method result is used as output.
// Resource marks the current method as an MCP resource provider.
func Resource(name, uri, mimeType string) {
	parent := eval.Current()
	method, isMethod := parent.(*goaexpr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}
	svc := method.Service
	var mcp *exprmcp.MCPExpr
	if r := exprmcp.Root; r != nil {
		mcp = r.GetMCP(svc)
	}
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	resource := &exprmcp.ResourceExpr{Name: name, Description: method.Description, URI: uri, MimeType: mimeType, Method: method}
	mcp.Resources = append(mcp.Resources, resource)
}

// WatchableResource marks the current method as an MCP resource that supports subscriptions.
func WatchableResource(name, uri, mimeType string) {
	parent := eval.Current()
	method, isMethod := parent.(*goaexpr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}
	svc := method.Service
	var mcp *exprmcp.MCPExpr
	if r := exprmcp.Root; r != nil {
		mcp = r.GetMCP(svc)
	}
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	resource := &exprmcp.ResourceExpr{Name: name, Description: method.Description, URI: uri, MimeType: mimeType, Method: method, Watchable: true}
	mcp.Resources = append(mcp.Resources, resource)
}

// StaticPrompt adds a static prompt template to the MCP server.
func StaticPrompt(name, description string, messages ...string) {
	var mcp *exprmcp.MCPExpr
	if svc, ok := eval.Current().(*goaexpr.ServiceExpr); ok {
		if r := exprmcp.Root; r != nil {
			mcp = r.GetMCP(svc)
		}
	}
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	prompt := &exprmcp.PromptExpr{Name: name, Description: description, Messages: make([]*exprmcp.MessageExpr, 0)}
	for i := 0; i < len(messages); i += 2 {
		if i+1 < len(messages) {
			prompt.Messages = append(prompt.Messages, &exprmcp.MessageExpr{Role: messages[i], Content: messages[i+1]})
		}
	}
	mcp.Prompts = append(mcp.Prompts, prompt)
}

// DynamicPrompt marks the current method as a dynamic prompt generator.
func DynamicPrompt(name, description string) {
	parent := eval.Current()
	method, isMethod := parent.(*goaexpr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}
	svc := method.Service
	prompt := &exprmcp.DynamicPromptExpr{Name: name, Description: description, Method: method}
	if r := exprmcp.Root; r != nil {
		r.RegisterDynamicPrompt(svc, prompt)
	}
}

// Notification marks the current method as an MCP notification sender.
func Notification(name, description string) {
	parent := eval.Current()
	method, isMethod := parent.(*goaexpr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}
	svc := method.Service
	var mcp *exprmcp.MCPExpr
	if r := exprmcp.Root; r != nil {
		mcp = r.GetMCP(svc)
	}
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	notif := &exprmcp.NotificationExpr{Name: name, Description: description, Method: method}
	mcp.Notifications = append(mcp.Notifications, notif)
}

// Subscription marks the current method as handling subscriptions for the given resource name.
func Subscription(resourceName string) {
	parent := eval.Current()
	method, isMethod := parent.(*goaexpr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}
	svc := method.Service
	var mcp *exprmcp.MCPExpr
	if r := exprmcp.Root; r != nil {
		mcp = r.GetMCP(svc)
	}
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	sub := &exprmcp.SubscriptionExpr{ResourceName: resourceName, Method: method}
	mcp.Subscriptions = append(mcp.Subscriptions, sub)
}

// SubscriptionMonitor marks the current method as an SSE monitor for subscription updates.
func SubscriptionMonitor(name string) {
	parent := eval.Current()
	method, isMethod := parent.(*goaexpr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}
	svc := method.Service
	var mcp *exprmcp.MCPExpr
	if r := exprmcp.Root; r != nil {
		mcp = r.GetMCP(svc)
	}
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	monitor := &exprmcp.SubscriptionMonitorExpr{Name: name, Method: method}
	mcp.SubscriptionMonitors = append(mcp.SubscriptionMonitors, monitor)
}
