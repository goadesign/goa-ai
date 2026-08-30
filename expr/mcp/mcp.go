// Package mcp defines the expression types used to represent MCP server
// configuration during Goa design evaluation. These types are populated during
// DSL execution and form the schema used for MCP protocol code generation.
package mcp

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

type (
	// MCPExpr defines MCP server configuration for a Goa service.
	MCPExpr struct {
		eval.Expression

		// Name is the MCP server name as advertised to MCP clients.
		Name string
		// Version is the server implementation version.
		Version string
		// Description provides a human-readable explanation of the
		// server's purpose.
		Description string
		// ProtocolVersion is the MCP protocol version this server
		// implements.
		ProtocolVersion string
		// Tools is the collection of tool expressions exposed by this
		// server.
		Tools []*ToolExpr
		// Resources is the collection of resource expressions exposed
		// by this server.
		Resources []*ResourceExpr
		// Prompts is the collection of static prompt expressions
		// exposed by this server.
		Prompts []*PromptExpr
		// Service is the Goa service expression this MCP server is
		// bound to.
		Service *expr.ServiceExpr
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
)

const (
	defaultProtocolVersion = "2025-06-18"
	jsonRPCRouteMessage    = `service %q must declare JSONRPC(func(){ POST(...) }) with a service-level path`
	// ResourceURIPattern requires the scheme that identifies an MCP resource.
	ResourceURIPattern = `^[a-zA-Z][a-zA-Z0-9+.-]*:.*`
)

var resourceURIPattern = regexp.MustCompile(ResourceURIPattern)

// EvalName returns the name used for evaluation.
func (m *MCPExpr) EvalName() string {
	return "MCP server for " + m.Service.Name
}

// Finalize finalizes the MCP expression
func (m *MCPExpr) Finalize() {
	if m.ProtocolVersion == "" {
		m.ProtocolVersion = defaultProtocolVersion
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
	if m.ProtocolVersion != "" && m.ProtocolVersion != defaultProtocolVersion {
		verr.Add(m, "protocol version must be %q", defaultProtocolVersion)
	}
	route := m.jsonRPCRoute()
	switch {
	case route == nil || route.Path == "":
		verr.Add(m, jsonRPCRouteMessage, m.Service.Name)
	case route.Method != http.MethodPost:
		verr.Add(m, jsonRPCRouteMessage+`; found %s %q`, m.Service.Name, route.Method, route.Path)
	}
	toolNames := make(map[string]struct{}, len(m.Tools))
	for _, t := range m.Tools {
		if _, exists := toolNames[t.Name]; t.Name != "" && exists {
			verr.Add(t, "tool name %q is used more than once", t.Name)
		}
		toolNames[t.Name] = struct{}{}
		if err := t.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}
	resourceURIs := make(map[string]struct{}, len(m.Resources))
	for _, r := range m.Resources {
		if _, exists := resourceURIs[r.URI]; r.URI != "" && exists {
			verr.Add(r, "resource URI %q is used more than once", r.URI)
		}
		resourceURIs[r.URI] = struct{}{}
		if err := r.Validate(); err != nil {
			var ve *eval.ValidationErrors
			if errors.As(err, &ve) {
				verr.Merge(ve)
			}
		}
	}
	promptNames := make(map[string]struct{}, len(m.Prompts))
	for _, p := range m.Prompts {
		if _, exists := promptNames[p.Name]; p.Name != "" && exists {
			verr.Add(p, "prompt name %q is used more than once", p.Name)
		}
		promptNames[p.Name] = struct{}{}
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
	if t.Method != nil && t.Method.IsStreaming() {
		verr.Add(t, "tool %q uses streaming method %q; MCP tools must return one result from one request", t.Name, t.Method.Name)
	}
	if t.Method != nil && hasValue(t.Method.Payload) && expr.AsObject(t.Method.Payload.Type) == nil {
		verr.Add(t, "tool %q method %q payload must be an object", t.Name, t.Method.Name)
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
	} else if !resourceURIPattern.MatchString(r.URI) {
		verr.Add(r, "resource URI %q must include a scheme", r.URI)
	} else if _, err := url.ParseRequestURI(r.URI); err != nil {
		verr.Add(r, "resource URI %q is invalid", r.URI)
	}
	if r.MimeType == "" {
		verr.Add(r, "resource MIME type is required")
	}
	if r.Method != nil && r.Method.IsStreaming() {
		verr.Add(r, "resource %q uses streaming method %q; MCP resources must return one result from one request", r.Name, r.Method.Name)
	}
	if r.Method != nil && hasValue(r.Method.Payload) {
		verr.Add(r, "resource %q method %q must not define a payload", r.Name, r.Method.Name)
	}
	if r.Method != nil && !hasValue(r.Method.Result) {
		verr.Add(r, "resource %q method %q must define a result", r.Name, r.Method.Name)
	}
	if r.Method != nil && hasValue(r.Method.Result) && r.MimeType != "" {
		mediaType, _, err := mime.ParseMediaType(r.MimeType)
		switch {
		case err != nil:
			verr.Add(r, "resource %q MIME type %q is invalid", r.Name, r.MimeType)
		case strings.HasPrefix(mediaType, "text/") && !isString(r.Method.Result.Type):
			verr.Add(
				r,
				"resource %q uses MIME type %q but method %q does not return a string",
				r.Name,
				r.MimeType,
				r.Method.Name,
			)
		case !strings.HasPrefix(mediaType, "text/") && mediaType != "application/json":
			verr.Add(r, "resource %q MIME type %q is not supported", r.Name, r.MimeType)
		}
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
	for index, message := range p.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			verr.Add(p, "prompt %q message %d role must be %q or %q", p.Name, index+1, "user", "assistant")
		}
	}
	if len(verr.Errors) > 0 {
		return verr
	}
	return nil
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

// hasValue reports whether a method payload or result carries application data.
func hasValue(attribute *expr.AttributeExpr) bool {
	return attribute != nil && attribute.Type != nil && attribute.Type != expr.Empty
}

// isString follows a named type to determine whether its value is a string.
func isString(dataType expr.DataType) bool {
	switch actual := dataType.(type) {
	case expr.Primitive:
		return actual == expr.String
	case *expr.UserTypeExpr:
		return isString(actual.Type)
	case *expr.ResultTypeExpr:
		return isString(actual.Type)
	default:
		return false
	}
}

// jsonRPCRoute returns the service-level route written in the Goa design.
func (m *MCPExpr) jsonRPCRoute() *expr.RouteExpr {
	for _, service := range expr.Root.API.JSONRPC.Services {
		if service.ServiceExpr == m.Service {
			return service.JSONRPCRoute
		}
	}
	return nil
}
