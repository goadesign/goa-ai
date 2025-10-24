package dsl

import (
	_ "goa.design/goa-ai/codegen" // Automatically registers the plugin with Goa
	mcpexpr "goa.design/goa-ai/expr"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// MCPServer enables Model Context Protocol (MCP) for the current service and
// sets MCP server metadata.
//
// Capabilities are auto-detected from the service design based on defined
// tools, resources, prompts, notifications and subscriptions.
//
// Use MCPServer inside a Service DSL. Option functions such as ProtocolVersion
// can be passed to further configure the MCP server.
//
// Example:
//
//	var _ = Service("assistant", func() {
//		Description("Assistant with MCP support")
//		mcp.MCPServer("assistant-mcp", "1.0.0", mcp.ProtocolVersion("2025-06-18"))
//
//		Method("analyze_text", func() {
//			Payload(func() { Attribute("text", String, "Text to analyze"); Required("text") })
//			Result(func() { Attribute("result", Any, "Analysis result"); Required("result") })
//			mcp.Tool("analyze_text", "Analyze text")
//			JSONRPC(func() {})
//		})
//	})
func MCPServer(name, version string, opts ...func(*mcpexpr.MCPExpr)) {
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

	// Apply options (e.g., ProtocolVersion)
	for _, o := range opts {
		if o != nil {
			o(mcp)
		}
	}

	// Register with the root
	if r := mcpexpr.Root; r != nil {
		r.RegisterMCP(svc, mcp)
	}
}

// ProtocolVersion sets the MCP protocol version (e.g., "2025-06-18") on the MCP
// server created by MCPServer.
//
// Use as an option to MCPServer:
//
//	mcp.MCPServer("assistant-mcp", "1.0.0", mcp.ProtocolVersion("2025-06-18"))
func ProtocolVersion(version string) func(*mcpexpr.MCPExpr) {
	return func(m *mcpexpr.MCPExpr) {
		m.ProtocolVersion = version
	}
}

// Tool marks the current method as an MCP tool.
//
// Call Tool inside a Method DSL to expose it to MCP clients. The method payload
// becomes the tool input schema and the method result becomes the tool output.
//
// Example:
//
//	Method("search_knowledge", func() {
//		Description("Search the knowledge base")
//		Payload(func() {
//			Attribute("query", String, "Search query"); Required("query")
//		})
//		Result(func() { Attribute("results", ArrayOf(Any), "Matches"); Required("results") })
//		mcp.Tool("search", "Search the knowledge base")
//		JSONRPC(func() {})
//	})
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

// Resource marks the current method as an MCP resource provider.
//
// The resource is identified by name and published at the given URI with the
// provided MIME type. The method result describes the resource schema.
//
// Example:
//
//	Method("list_documents", func() {
//		Description("List available documents")
//		Result(Documents)
//		mcp.Resource("documents", "doc://list", "application/json")
//		JSONRPC(func() {})
//	})
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

// WatchableResource marks the current method as an MCP resource that supports
// subscriptions. The resource is identified by name and published at the given
// URI with the provided MIME type.
func WatchableResource(name, uri, mimeType string) {
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
		Watchable:   true,
	}
	mcp.Resources = append(mcp.Resources, resource)
}

// StaticPrompt adds a static prompt template to the MCP server.
//
// Messages are specified as role/content pairs in order: "system", content,
// "user", content, etc. Use inside a Service DSL after MCPServer.
//
// Example:
//
//	mcp.StaticPrompt(
//		"code_review",
//		"Template for code review",
//		"system", "You are an expert code reviewer.",
//		"user", "Please review this code: {{.code}}",
//	)
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

// DynamicPrompt marks the current method as a dynamic prompt generator.
//
// The method payload and result describe inputs and generated prompt templates,
// respectively. Use inside a Method DSL.
//
// Example:
//
//	Method("generate_prompts", func() {
//		Description("Generate context-aware prompts")
//		Payload(func() { Attribute("context", String, "Current context"); Required("context") })
//		Result(PromptTemplates)
//		mcp.DynamicPrompt("contextual_prompts", "Generate prompts based on context")
//		JSONRPC(func() {})
//	})
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

// Notification marks the current method as an MCP notification sender.
//
// Use inside a Method DSL for a method that sends notifications to clients.
// The method payload defines the notification content schema.
//
// Example:
//
//	Method("send_notification", func() {
//		Description("Send status notification to client")
//		Payload(func() {
//			Attribute("type", String, "Notification type"); Attribute("message", String, "Message")
//			Required("type", "message")
//		})
//		mcp.Notification("status_update", "Send status updates to client")
//		JSONRPC(func() {})
//	})
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

// Subscription marks the current method as handling subscriptions for the given
// resource name.
//
// Use inside a Method DSL that establishes a subscription and returns
// subscription info.
//
// Example:
//
//	Method("subscribe_to_updates", func() {
//		Description("Subscribe to resource updates")
//		Payload(func() { Attribute("resource", String, "Resource"); Required("resource") })
//		Result(SubscriptionInfo)
//		mcp.Subscription("documents")
//		JSONRPC(func() {})
//	})
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

// SubscriptionMonitor marks the current method as an SSE monitor for
// subscription updates.
//
// Use inside a Method DSL that streams updates to clients via Server-Sent
// Events (SSE).
//
// Example:
//
//	Method("monitor_resource_changes", func() {
//		Description("Monitor resource changes with server streaming")
//		StreamingResult(ResourceUpdate)
//		mcp.SubscriptionMonitor("documents_monitor")
//		HTTP(func() { GET("/stream/monitor/{resource_type}") })
//	})
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
