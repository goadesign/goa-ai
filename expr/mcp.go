package expr

import (
	"errors"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// MCPExpr defines MCP server configuration
type MCPExpr struct {
	// Name is the server name
	Name string
	// Version is the server version
	Version string
	// Description is the server description
	Description string
	// ProtocolVersion is the MCP protocol version (e.g., 2025-06-18)
	ProtocolVersion string
	// Transport defines the transport mechanism (stdio, http, sse)
	Transport string
	// Capabilities defines server capabilities
	Capabilities *CapabilitiesExpr
	// Tools is the list of tools exposed by the server
	Tools []*ToolExpr
	// Resources is the list of resources exposed by the server
	Resources []*ResourceExpr
	// Prompts is the list of prompts exposed by the server
	Prompts []*PromptExpr
	// Notifications that the server can send
	Notifications []*NotificationExpr
	// Subscriptions for resource updates
	Subscriptions []*SubscriptionExpr
	// SubscriptionMonitors for SSE streaming of subscription updates
	SubscriptionMonitors []*SubscriptionMonitorExpr
	// Service is the parent service
	Service *expr.ServiceExpr
	// Parent expression
	eval.Expression
}

// CapabilitiesExpr defines MCP server capabilities
type CapabilitiesExpr struct {
	// Server capabilities
	// EnableTools indicates if tools are supported
	EnableTools bool
	// EnableResources indicates if resources are supported
	EnableResources bool
	// EnablePrompts indicates if prompts are supported
	EnablePrompts bool
	// EnableLogging indicates if logging is supported
	EnableLogging bool
	// EnableProgress indicates if progress tracking is supported
	EnableProgress bool
	// EnableCancellation indicates if cancellation is supported
	EnableCancellation bool

	// Additional capabilities
	// EnableNotifications indicates if server sends notifications
	EnableNotifications bool
	// EnableCompletion indicates if server supports completion/autocomplete
	EnableCompletion bool
	// EnablePagination indicates if server supports paginated responses
	EnablePagination bool
	// EnableSubscriptions indicates if server supports resource subscriptions
	EnableSubscriptions bool

	// Parent expression
	eval.Expression
}

// ToolExpr defines an MCP tool
type ToolExpr struct {
	// Name is the tool name
	Name string
	// Description is the tool description
	Description string
	// Method is the Goa method that implements the tool
	Method *expr.MethodExpr
	// InputSchema defines the tool input schema
	InputSchema *expr.AttributeExpr
	// Parent expression
	eval.Expression
}

// ResourceExpr defines an MCP resource
type ResourceExpr struct {
	// Name is the resource name
	Name string
	// Description is the resource description
	Description string
	// URI is the resource URI template
	URI string
	// MimeType is the resource MIME type
	MimeType string
	// Method is the Goa method that provides the resource
	Method *expr.MethodExpr
	// Watchable indicates whether the resource supports subscriptions
	Watchable bool
	// Parent expression
	eval.Expression
}

// PromptExpr defines an MCP prompt
type PromptExpr struct {
	// Name is the prompt name
	Name string
	// Description is the prompt description
	Description string
	// Arguments defines the prompt arguments
	Arguments *expr.AttributeExpr
	// Messages defines the prompt messages
	Messages []*MessageExpr
	// Parent expression
	eval.Expression
}

// MessageExpr defines a prompt message
type MessageExpr struct {
	// Role is the message role (user, assistant, system)
	Role string
	// Content is the message content template
	Content string
	// Parent expression
	eval.Expression
}

// DynamicPromptExpr defines a dynamic prompt
type DynamicPromptExpr struct {
	// Name is the dynamic prompt name
	Name string
	// Description is the dynamic prompt description
	Description string
	// Method is the Goa method that generates prompts
	Method *expr.MethodExpr
	// Parent expression
	eval.Expression
}

// (removed) SamplingExpr

// (removed) RootsExpr

// NotificationExpr defines a notification
type NotificationExpr struct {
	// Name is the notification name
	Name string
	// Description describes the notification
	Description string
	// Method is the Goa method that sends the notification
	Method *expr.MethodExpr
	// Parent expression
	eval.Expression
}

// SubscriptionExpr defines a resource subscription
type SubscriptionExpr struct {
	// ResourceName is the resource to subscribe to
	ResourceName string
	// Method is the Goa method that handles subscriptions
	Method *expr.MethodExpr
	// Parent expression
	eval.Expression
}

// SubscriptionMonitorExpr defines a subscription monitor for SSE
type SubscriptionMonitorExpr struct {
	// Name is the monitor name
	Name string
	// Method is the Goa method that monitors updates
	Method *expr.MethodExpr
	// Parent expression
	eval.Expression
}

// LogStreamingExpr defines log streaming via SSE
type LogStreamingExpr struct {
	// Name is the log stream name
	Name string
	// Method is the Goa method that streams logs
	Method *expr.MethodExpr
	// Parent expression
	eval.Expression
}

// EvalName returns the name used for evaluation
func (m *MCPExpr) EvalName() string {
	return "MCP server for " + m.Service.Name
}

// Finalize finalizes the MCP expression
func (m *MCPExpr) Finalize() {
	// Validate transport
	if m.Transport == "" {
		m.Transport = "jsonrpc" // Default transport per plugin
	}

	// Initialize capabilities if not set
	if m.Capabilities == nil {
		m.Capabilities = &CapabilitiesExpr{}
	}

	// Auto-detect capabilities based on defined elements
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

	// Validate tools
	for _, tool := range m.Tools {
		if err := tool.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}

	// Validate resources
	for _, resource := range m.Resources {
		if err := resource.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}

	// Validate prompts
	for _, prompt := range m.Prompts {
		if err := prompt.Validate(); err != nil {
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

// EvalName returns the name for other expressions
func (c *CapabilitiesExpr) EvalName() string { return "MCP capabilities" }

// EvalName returns the DSL evaluation name for the tool expression.
func (t *ToolExpr) EvalName() string { return "MCP tool " + t.Name }

// EvalName returns the DSL evaluation name for the resource expression.
func (r *ResourceExpr) EvalName() string { return "MCP resource " + r.Name }

// EvalName returns the DSL evaluation name for the prompt expression.
func (p *PromptExpr) EvalName() string { return "MCP prompt " + p.Name }

// EvalName returns the DSL evaluation name for the message expression.
func (m *MessageExpr) EvalName() string { return "MCP message" }

// EvalName returns the DSL evaluation name for the dynamic prompt expression.
func (d *DynamicPromptExpr) EvalName() string { return "MCP dynamic prompt " + d.Name }
