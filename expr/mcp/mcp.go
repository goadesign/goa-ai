// Package mcp defines the expression types used to represent MCP server
// configuration during Goa design evaluation. These types are populated during
// DSL execution and form the schema used for MCP protocol code generation.
package mcp

import (
	"errors"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// MCPExpr defines MCP server configuration for a Goa service.
	MCPExpr struct {
		eval.Expression

		// Name is the MCP server name (suite identifier).
		Name string
		// Version is the server implementation version.
		Version string
		// Description provides a human-readable explanation of the
		// server's purpose.
		Description string
		// ProtocolVersion is the MCP protocol version this server
		// implements.
		ProtocolVersion string
		// Transport is the transport mechanism (e.g., "jsonrpc",
		// "sse").
		Transport string
		// Capabilities defines which MCP capabilities this server
		// supports.
		Capabilities *CapabilitiesExpr
		// Tools is the collection of tool expressions exposed by this
		// server.
		Tools []*ToolExpr
		// Resources is the collection of resource expressions exposed
		// by this server.
		Resources []*ResourceExpr
		// Prompts is the collection of static prompt expressions
		// exposed by this server.
		Prompts []*PromptExpr
		// Notifications is the collection of notification expressions
		// this server can send.
		Notifications []*NotificationExpr
		// Subscriptions is the collection of resource subscription
		// expressions this server supports.
		Subscriptions []*SubscriptionExpr
		// SubscriptionMonitors is the collection of subscription
		// monitor expressions for SSE.
		SubscriptionMonitors []*SubscriptionMonitorExpr
		// Service is the Goa service expression this MCP server is
		// bound to.
		Service *expr.ServiceExpr
	}

	// CapabilitiesExpr defines which MCP protocol capabilities a server supports.
	CapabilitiesExpr struct {
		eval.Expression

		// EnableTools indicates whether the server exposes tool
		// invocation.
		EnableTools bool
		// EnableResources indicates whether the server exposes resource
		// access.
		EnableResources bool
		// EnablePrompts indicates whether the server exposes prompt
		// templates.
		EnablePrompts bool
		// EnableLogging indicates whether the server supports logging.
		EnableLogging bool
		// EnableProgress indicates whether the server supports progress
		// notifications.
		EnableProgress bool
		// EnableCancellation indicates whether the server supports
		// request cancellation.
		EnableCancellation bool
		// EnableNotifications indicates whether the server can send
		// notifications.
		EnableNotifications bool
		// EnableCompletion indicates whether the server supports
		// completion suggestions.
		EnableCompletion bool
		// EnablePagination indicates whether the server supports
		// paginated responses.
		EnablePagination bool
		// EnableSubscriptions indicates whether the server supports
		// resource subscriptions.
		EnableSubscriptions bool
	}

	// ToolExpr defines an MCP tool that the server exposes for invocation.
	ToolExpr struct {
		eval.Expression

		// Name is the unique identifier for this tool.
		Name string
		// Description provides a human-readable explanation of what the
		// tool does.
		Description string
		// Method is the Goa service method that implements this tool.
		Method *expr.MethodExpr
		// InputSchema defines the parameter schema for this tool.
		InputSchema *expr.AttributeExpr
	}

	// ResourceExpr defines an MCP resource that the server exposes for access.
	ResourceExpr struct {
		eval.Expression

		// Name is the unique identifier for this resource.
		Name string
		// Description provides a human-readable explanation of the
		// resource.
		Description string
		// URI is the resource identifier used for access.
		URI string
		// MimeType is the MIME type of the resource content.
		MimeType string
		// Method is the Goa service method that provides this resource.
		Method *expr.MethodExpr
		// Watchable indicates whether this resource supports change
		// notifications.
		Watchable bool
	}

	// PromptExpr defines a static MCP prompt template exposed by the
	// server.
	PromptExpr struct {
		eval.Expression

		// Name is the unique identifier for this prompt.
		Name string
		// Description provides a human-readable explanation of the
		// prompt's purpose.
		Description string
		// Arguments defines the parameter schema for this prompt
		// template.
		Arguments *expr.AttributeExpr
		// Messages is the collection of message templates in this
		// prompt.
		Messages []*MessageExpr
	}

	// MessageExpr defines a single message within a prompt template.
	MessageExpr struct {
		eval.Expression

		// Role is the message sender role (e.g., "user", "assistant").
		Role string
		// Content is the message text content or template.
		Content string
	}

	// DynamicPromptExpr defines a dynamic prompt generated at runtime by a
	// service method.
	DynamicPromptExpr struct {
		eval.Expression

		// Name is the unique identifier for this dynamic prompt.
		Name string
		// Description provides a human-readable explanation of the prompt's
		// purpose.
		Description string
		// Method is the Goa service method that generates this prompt.
		Method *expr.MethodExpr
	}

	// NotificationExpr defines a notification that the server can send to
	// clients.
	NotificationExpr struct {
		eval.Expression

		// Name is the unique identifier for this notification type.
		Name string
		// Description provides a human-readable explanation of the
		// notification.
		Description string
		// Method is the Goa service method that sends this notification.
		Method *expr.MethodExpr
	}

	// SubscriptionExpr defines a subscription to resource change events.
	SubscriptionExpr struct {
		eval.Expression

		// ResourceName is the name of the resource being subscribed to.
		ResourceName string
		// Method is the Goa service method that handles this subscription.
		Method *expr.MethodExpr
	}

	// SubscriptionMonitorExpr defines a subscription monitor for SSE-based
	// subscriptions.
	SubscriptionMonitorExpr struct {
		eval.Expression

		// Name is the unique identifier for this monitor.
		Name string
		// Method is the Goa service method that implements the monitor.
		Method *expr.MethodExpr
	}
)

// EvalName returns the name used for evaluation.
func (m *MCPExpr) EvalName() string {
	return "MCP server for " + m.Service.Name
}

// Finalize finalizes the MCP expression
func (m *MCPExpr) Finalize() {
	if m.Transport == "" {
		m.Transport = "jsonrpc"
	}
	if m.Capabilities == nil {
		m.Capabilities = &CapabilitiesExpr{}
	}
	if len(m.Tools) > 0 {
		m.Capabilities.EnableTools = true
	}
	if len(m.Resources) > 0 {
		m.Capabilities.EnableResources = true
	}
	if len(m.Prompts) > 0 {
		m.Capabilities.EnablePrompts = true
	}
}

// Validate validates the MCP expression
func (m *MCPExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if m.Name == "" {
		verr.Add(m, "MCP server name is required")
	}
	if m.Version == "" {
		verr.Add(m, "MCP server version is required")
	}
	for _, t := range m.Tools {
		if err := t.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}
	for _, r := range m.Resources {
		if err := r.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}
	for _, p := range m.Prompts {
		if err := p.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// Validate validates a tool expression
func (t *ToolExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if t.Name == "" {
		verr.Add(t, "tool name is required")
	}
	if t.Description == "" {
		verr.Add(t, "tool description is required")
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// Validate validates a resource expression
func (r *ResourceExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if r.Name == "" {
		verr.Add(r, "resource name is required")
	}
	if r.URI == "" {
		verr.Add(r, "resource URI is required")
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// Validate validates a prompt expression
func (p *PromptExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if p.Name == "" {
		verr.Add(p, "prompt name is required")
	}
	if len(p.Messages) == 0 {
		verr.Add(p, "prompt must have at least one message")
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
}

// EvalName returns the name used for evaluation.
func (c *CapabilitiesExpr) EvalName() string {
	return "MCP capabilities"
}

// EvalName returns the name used for evaluation.
func (t *ToolExpr) EvalName() string {
	return "MCP tool " + t.Name
}

// EvalName returns the name used for evaluation.
func (r *ResourceExpr) EvalName() string {
	return "MCP resource " + r.Name
}

// EvalName returns the name used for evaluation.
func (p *PromptExpr) EvalName() string {
	return "MCP prompt " + p.Name
}

// EvalName returns the name used for evaluation.
func (m *MessageExpr) EvalName() string {
	return "MCP message"
}

// EvalName returns the name used for evaluation.
func (d *DynamicPromptExpr) EvalName() string {
	return "MCP dynamic prompt " + d.Name
}
