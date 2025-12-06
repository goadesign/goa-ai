package dsl

import (
	_ "goa.design/goa-ai/codegen/mcp" // Registers the MCP codegen plugin with Goa
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// MCP enables Model Context Protocol (MCP) support for the current service.
// It configures the service to expose tools, resources, and prompts via the MCP
// protocol. Once enabled, use Resource, Tool (in Method context), and related
// DSL functions within service methods to define MCP capabilities.
//
// MCP must appear in a Service expression.
//
// MCP takes two required arguments and an optional list of configuration
// functions:
//   - name: the MCP server name (used in MCP handshake)
//   - version: the server version string
//   - opts: optional configuration functions (e.g., ProtocolVersion)
//
// Example:
//
//	Service("calculator", func() {
//	    MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
//	    Method("add", func() {
//	        Payload(func() {
//	            Attribute("a", Int)
//	            Attribute("b", Int)
//	        })
//	        Result(func() {
//	            Attribute("sum", Int)
//	        })
//	        Tool("add", "Add two numbers")
//	    })
//	})
func MCP(name, version string, opts ...func(*exprmcp.MCPExpr)) {
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

// ProtocolVersion configures the MCP protocol version supported by the server.
// It returns a configuration function for use with MCP.
//
// ProtocolVersion takes a single argument which is the protocol version string.
//
// Example:
//
//	Service("calculator", func() {
//	    MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
//	})
func ProtocolVersion(version string) func(*exprmcp.MCPExpr) {
	return func(m *exprmcp.MCPExpr) { m.ProtocolVersion = version }
}

// Resource marks the current method as an MCP resource provider. The method's
// result becomes the resource content returned when clients read the resource.
//
// Resource must appear in a Method expression within a service that has MCP enabled.
//
// Resource takes three arguments:
//   - name: the resource name (used in MCP resource list)
//   - uri: the resource URI (e.g., "file:///docs/readme.md")
//   - mimeType: the content MIME type (e.g., "text/plain", "application/json")
//
// Example:
//
//	Method("readme", func() {
//	    Result(String)
//	    Resource("readme", "file:///docs/README.md", "text/markdown")
//	})
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

// WatchableResource marks the current method as an MCP resource that supports
// subscriptions. Clients can subscribe to receive notifications when the resource
// content changes.
//
// WatchableResource must appear in a Method expression within a service that has
// MCP enabled.
//
// WatchableResource takes three arguments:
//   - name: the resource name (used in MCP resource list)
//   - uri: the resource URI (e.g., "file:///logs/app.log")
//   - mimeType: the content MIME type (e.g., "text/plain")
//
// Example:
//
//	Method("system_status", func() {
//	    Result(func() {
//	        Attribute("status", String)
//	        Attribute("uptime", Int)
//	    })
//	    WatchableResource("status", "status://system", "application/json")
//	})
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

// StaticPrompt adds a static prompt template to the MCP server. Static prompts
// provide pre-defined message sequences that clients can use without parameters.
//
// StaticPrompt must appear in a Service expression with MCP enabled.
//
// StaticPrompt takes a name, description, and a list of role-content pairs:
//   - name: the prompt identifier
//   - description: human-readable prompt description
//   - messages: alternating role and content strings (e.g., "user", "text", "system", "text")
//
// Example:
//
//	Service("assistant", func() {
//	    MCP("assistant", "1.0")
//	    StaticPrompt("greeting", "Friendly greeting",
//	        "system", "You are a helpful assistant",
//	        "user", "Hello!")
//	})
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

// DynamicPrompt marks the current method as a dynamic prompt generator. The
// method's payload defines parameters that customize the generated prompt, and
// the result contains the generated message sequence.
//
// DynamicPrompt must appear in a Method expression within a service that has MCP enabled.
//
// DynamicPrompt takes two arguments:
//   - name: the prompt identifier
//   - description: human-readable prompt description
//
// Example:
//
//	Method("code_review", func() {
//	    Payload(func() {
//	        Attribute("language", String)
//	        Attribute("code", String)
//	    })
//	    Result(ArrayOf(Message))
//	    DynamicPrompt("code_review", "Generate code review prompt")
//	})
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

// Notification marks the current method as an MCP notification sender. The
// method's payload defines the notification message structure.
//
// Notification must appear in a Method expression within a service that has MCP enabled.
//
// Notification takes two arguments:
//   - name: the notification identifier
//   - description: human-readable notification description
//
// Example:
//
//	Method("progress_update", func() {
//	    Payload(func() {
//	        Attribute("task_id", String)
//	        Attribute("progress", Int)
//	    })
//	    Notification("progress", "Task progress notification")
//	})
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

// Subscription marks the current method as a subscription handler for a
// watchable resource. The method is invoked when clients subscribe to the
// resource identified by resourceName.
//
// Subscription must appear in a Method expression within a service that has MCP enabled.
//
// Subscription takes a single argument which is the resource name to subscribe to.
// The resource name must match a WatchableResource declaration.
//
// Example:
//
//	Method("subscribe_status", func() {
//	    Payload(func() {
//	        Attribute("uri", String)
//	    })
//	    Result(String)
//	    Subscription("status")
//	})
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

// SubscriptionMonitor marks the current method as a server-sent events (SSE)
// monitor for subscription updates. The method streams subscription change events
// to connected clients.
//
// SubscriptionMonitor must appear in a Method expression within a service that has MCP enabled.
//
// SubscriptionMonitor takes a single argument which is the monitor name.
//
// Example:
//
//	Method("watch_subscriptions", func() {
//	    StreamingResult(func() {
//	        Attribute("resource", String)
//	        Attribute("event", String)
//	    })
//	    SubscriptionMonitor("subscriptions")
//	})
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
