package dsl

import (
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
	_ "goa.design/plugins/v3/mcp"      // Import plugin to register with eval
	mcpexpr "goa.design/plugins/v3/mcp/expr"
)

// MCPServer enables MCP for the current service
// Capabilities are automatically detected based on defined tools, resources, etc.
func MCPServer(name, version string) {
	svc, ok := eval.Current().(*expr.ServiceExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}

	mcp := &mcpexpr.MCPExpr{
		Service:      svc,
		Name:         name,
		Version:      version,
		Description:  svc.Description,
		Capabilities: &mcpexpr.CapabilitiesExpr{},
	}

	// Register with the root
	if r := mcpexpr.Root; r != nil {
		r.RegisterMCP(svc, mcp)
	}
}

// Tool marks a method as an MCP tool
func Tool(name, description string) {
	parent := eval.Current()
	method, isMethod := parent.(*expr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}

	svc := method.Service
	var mcp *mcpexpr.MCPExpr
	if r := mcpexpr.Root; r != nil {
		mcp = r.GetMCP(svc)
	}

	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}

	tool := &mcpexpr.ToolExpr{
		Name:        name,
		Description: description,
		Method:      method,
	}

	// Use method payload as input schema
	if method.Payload != nil {
		tool.InputSchema = method.Payload
	}

	// Set expression for proper error reporting
	tool.Expression = method

	mcp.Tools = append(mcp.Tools, tool)
}

// Resource marks a method as an MCP resource provider
func Resource(name, uri, mimeType string) {
	parent := eval.Current()
	method, isMethod := parent.(*expr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}

	svc := method.Service
	var mcp *mcpexpr.MCPExpr
	if r := mcpexpr.Root; r != nil {
		mcp = r.GetMCP(svc)
	}

	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}

	resource := &mcpexpr.ResourceExpr{
		Name:        name,
		Description: method.Description,
		URI:         uri,
		MimeType:    mimeType,
		Method:      method,
	}

	mcp.Resources = append(mcp.Resources, resource)
}

// StaticPrompt adds a static prompt template to the MCP server
func StaticPrompt(name, description string, messages ...string) {
	// Find the current service's MCP config
	var mcp *mcpexpr.MCPExpr
	if svc, ok := eval.Current().(*expr.ServiceExpr); ok {
		if r := mcpexpr.Root; r != nil {
			mcp = r.GetMCP(svc)
		}
	}

	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}

	prompt := &mcpexpr.PromptExpr{
		Name:        name,
		Description: description,
		Messages:    make([]*mcpexpr.MessageExpr, 0),
	}

	// Parse messages (role:content format)
	for i := 0; i < len(messages); i += 2 {
		if i+1 < len(messages) {
			msg := &mcpexpr.MessageExpr{
				Role:    messages[i],
				Content: messages[i+1],
			}
			prompt.Messages = append(prompt.Messages, msg)
		}
	}

	mcp.Prompts = append(mcp.Prompts, prompt)
}

// DynamicPrompt marks a method as a dynamic prompt generator
func DynamicPrompt(name, description string) {
	parent := eval.Current()
	method, isMethod := parent.(*expr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}

	svc := method.Service
	prompt := &mcpexpr.DynamicPromptExpr{
		Name:        name,
		Description: description,
		Method:      method,
	}

	// Register with the root
	if r := mcpexpr.Root; r != nil {
		r.RegisterDynamicPrompt(svc, prompt)
	}
}

// Notification marks a method as sending notifications
func Notification(name, description string) {
	parent := eval.Current()
	method, isMethod := parent.(*expr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}

	svc := method.Service
	var mcp *mcpexpr.MCPExpr
	if r := mcpexpr.Root; r != nil {
		mcp = r.GetMCP(svc)
	}

	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}

	notif := &mcpexpr.NotificationExpr{
		Name:        name,
		Description: description,
		Method:      method,
	}

	mcp.Notifications = append(mcp.Notifications, notif)
}

// Subscription marks a method as handling resource subscriptions
func Subscription(resourceName string) {
	parent := eval.Current()
	method, isMethod := parent.(*expr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}

	svc := method.Service
	var mcp *mcpexpr.MCPExpr
	if r := mcpexpr.Root; r != nil {
		mcp = r.GetMCP(svc)
	}

	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}

	sub := &mcpexpr.SubscriptionExpr{
		ResourceName: resourceName,
		Method:       method,
	}

	mcp.Subscriptions = append(mcp.Subscriptions, sub)
}

// SubscriptionMonitor marks a method as monitoring subscription updates via SSE
func SubscriptionMonitor(name string) {
	parent := eval.Current()
	method, isMethod := parent.(*expr.MethodExpr)
	if !isMethod {
		eval.IncompatibleDSL()
		return
	}

	svc := method.Service
	var mcp *mcpexpr.MCPExpr
	if r := mcpexpr.Root; r != nil {
		mcp = r.GetMCP(svc)
	}

	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}

	monitor := &mcpexpr.SubscriptionMonitorExpr{
		Name:   name,
		Method: method,
	}

	mcp.SubscriptionMonitors = append(mcp.SubscriptionMonitors, monitor)
}
