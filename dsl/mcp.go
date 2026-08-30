// Package dsl exposes the MCP design functions that authors call inside Goa
// services and methods.
package dsl

import (
	exprmcp "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// MCP enables Model Context Protocol (MCP) support for the current service.
// It configures the service to expose tools, resources, and prompts via the MCP
// protocol. Once enabled, use Resource, Tool (in Method context), and related
// DSL functions within service methods to define MCP capabilities.
//
// MCP must appear in a Service expression. The service-level JSONRPC POST
// route supplies the MCP path. The same service may also expose ordinary HTTP,
// file, and gRPC endpoints.
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
//	    JSONRPC(func() {
//	        POST("/mcp")
//	    })
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
	m := &exprmcp.MCPExpr{Service: svc, Name: name, Version: version, Description: svc.Description}
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
//	    JSONRPC(func() {
//	        POST("/mcp")
//	    })
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

// StaticPrompt adds a static prompt template to the MCP server. Static prompts
// provide pre-defined message sequences that clients can use without parameters.
//
// StaticPrompt must appear in a Service expression with MCP enabled.
//
// StaticPrompt takes a name, description, and a list of role-content pairs:
//   - name: the prompt identifier
//   - description: human-readable prompt description
//   - messages: alternating role and content strings (e.g., "user", "text", "assistant", "text")
//
// Example:
//
//	Service("assistant", func() {
//	    MCP("assistant", "1.0")
//	    JSONRPC(func() {
//	        POST("/mcp")
//	    })
//	    StaticPrompt("greeting", "Friendly greeting",
//	        "user", "You are a helpful assistant",
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
	if len(messages)%2 != 0 {
		eval.ReportError("StaticPrompt requires role/content pairs")
		return
	}
	prompt := &exprmcp.PromptExpr{Name: name, Description: description, Messages: make([]*exprmcp.MessageExpr, 0)}
	for i := 0; i < len(messages); i += 2 {
		prompt.Messages = append(prompt.Messages, &exprmcp.MessageExpr{Role: messages[i], Content: messages[i+1]})
	}
	mcp.Prompts = append(mcp.Prompts, prompt)
}
