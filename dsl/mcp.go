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
// Register the generated server with the same mux passed to its constructor,
// or call Mount(mux), so Goa supplies route parameters to generated decoders.
//
// MCP takes two required arguments and an optional design function. Security
// inside that function declares bearer access and basic-access scopes for every
// MCP request, including catalogs. The generated server then requires a runtime
// ResourceServer; original method authentication still runs through Goa.
//   - name: the server name returned in response metadata
//   - version: the server version string
//
// Example:
//
//	Service("calculator", func() {
//	    MCP("calc", "1.0.0")
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
func MCP(name, version string, design ...func()) {
	svc, ok := eval.Current().(*goaexpr.ServiceExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	m := &exprmcp.MCPExpr{Service: svc, Name: name, Version: version, Description: svc.Description}
	if len(design) > 1 {
		eval.TooManyArgError()
		return
	}
	if len(design) == 1 && !eval.Execute(design[0], m) {
		return
	}
	if r := exprmcp.Root; r != nil {
		r.RegisterMCP(svc, m)
	}
}

// Resource marks the current method as an MCP resource provider. The method's
// result becomes the resource content returned when clients read the resource.
// Bytes become base64 blob content with the declared MIME type. A string with a
// text MIME type becomes text unchanged; application/json results use the generated
// JSON codec. The generator chooses the representation from the declared result.
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

// Prompt exposes the current Goa method through MCP prompts/get. Its payload
// must be an object of named strings. Its result must contain a messages array;
// each message has a role and a content OneOf whose branches are text, image,
// audio, resource_link, or resource. Each branch declares its content fields.
// Image and audio data, and embedded resource blobs, use Bytes. Generated code
// validates the result and converts bytes to base64 for the MCP response.
//
// Prompt must appear in a Method expression within an MCP service. It does not
// call a model: the service supplies messages when a client selects the prompt.
func Prompt(name, description string) {
	method, ok := eval.Current().(*goaexpr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	mcp := exprmcp.Root.GetMCP(method.Service)
	if mcp == nil {
		eval.IncompatibleDSL()
		return
	}
	mcp.MethodPrompts = append(mcp.MethodPrompts, &exprmcp.MethodPromptExpr{
		Name: name, Description: description, Method: method,
	})
}
