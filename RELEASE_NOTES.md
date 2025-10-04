# Goa-AI v0.1.0 Release Notes

**The Design-First Toolkit for AI Backends**

We're excited to announce the first release of **Goa-AI**, a powerful plugin for the [Goa framework](https://goa.design) that brings design-first development to AI backends. With Goa-AI, you can build production-ready [Model Context Protocol (MCP)](https://modelcontextprotocol.io) servers from simple Go definitions—no more handwriting API specs, JSON schemas, or boilerplate code.

## 🎯 What is Goa-AI?

Goa-AI extends Goa's proven design-first methodology to the world of AI agents. Define your AI backend's tools, resources, and prompts in a simple, type-safe Go DSL, and let Goa-AI generate:

- **Complete MCP server** with JSON-RPC transport
- **Strongly-typed JSON schemas** for tool definitions
- **Server-Sent Events (SSE)** for streaming responses
- **Client libraries** with type-safe wrappers
- **Protocol compliance** with automatic capability negotiation
- **Production-ready code** with robust error handling

## ✨ Key Features

### 🛠️ MCP Protocol Implementation

Complete implementation of the Model Context Protocol specification:

- **Tools**: Expose service methods as AI-callable tools with automatic JSON schema generation
- **Resources**: Provide data resources with URI templates and MIME type support
- **Prompts**: Define static prompt templates or dynamic prompt generators
- **Notifications**: Send server-initiated notifications to clients
- **Subscriptions**: Real-time resource updates via Server-Sent Events
- **Streaming**: First-class support for streaming tool responses

### 🎨 Elegant DSL

Simple, readable API for describing your AI backend:

```go
var _ = Service("assistant", func() {
    mcp.MCPServer("assistant-mcp", "1.0.0")
    
    Method("analyze_text", func() {
        Payload(func() {
            Attribute("text", String, "Text to analyze")
            Required("text")
        })
        Result(AnalysisResult)
        mcp.Tool("analyze_text", "Analyze text for sentiment")
    })
})
```

### 🔄 JSON-RPC Transport

Built on Goa's robust JSON-RPC implementation:

- Automatic request/response encoding/decoding
- Structured error codes and messages
- SSE for streaming responses
- Content negotiation via Accept headers
- Goa-generated encoders (no manual JSON marshaling)

### 🧩 Flexible Architecture

- **Adapter pattern**: Clean separation between MCP protocol and your business logic
- **Configurable options**: Logging, error mapping, resource access control
- **Client wrapper**: Incremental MCP adoption for existing services
- **Protocol versioning**: Explicit version negotiation and validation

### 🎯 Type Safety

- Go type system ensures consistency between server and JSON schemas
- Compile-time checks prevent runtime errors
- Goa validations enforce contracts at API boundaries
- No redundant nil checks or defensive programming needed

### 🧪 Comprehensive Testing

Production-ready with extensive integration tests:

- 25+ test scenarios covering all protocol features
- YAML-based test definitions for readability
- Framework for adding custom scenarios
- CI/CD integration with GitHub Actions

## 📦 What's Included

### Core Components

- **DSL Package** (`dsl/`): Clean, expressive API for defining MCP servers
  - `MCPServer()` - Enable MCP for a service
  - `Tool()` - Expose methods as AI tools
  - `Resource()` - Define data resources
  - `StaticPrompt()` / `DynamicPrompt()` - Prompt templates
  - `Notification()` - Server notifications
  - `Subscription()` / `SubscriptionMonitor()` - Resource subscriptions

- **Code Generators** (`codegen/`): Transform designs into production code
  - Service layer generation (endpoints, client, types)
  - JSON-RPC transport with SSE support
  - MCP adapter for protocol handling
  - Prompt provider interface
  - Client wrapper for mixed services

- **Expression Builder** (`expr/`): Internal representation of MCP concepts
  - Capability detection from defined features
  - Tool input schema derivation
  - Resource URI template handling
  - Protocol version validation

### Generated Artifacts

For each MCP-enabled service, Goa-AI generates:

```
gen/
├── mcp_assistant/              # Service layer
│   ├── service.go             # Service interface
│   ├── endpoints.go           # Endpoint wiring
│   ├── client.go              # Client implementation
│   ├── adapter_server.go      # MCP protocol adapter
│   ├── prompt_provider.go     # Prompt interface
│   └── protocol_version.go    # Version constants
└── jsonrpc/
    └── mcp_assistant/          # Transport layer
        ├── client/            # JSON-RPC client
        ├── server/            # JSON-RPC server with SSE
        └── ...
```

### Example Service

Complete working example demonstrating all features:

- Tools: text analysis, code execution, batch processing
- Resources: documents, system info with subscriptions
- Prompts: static templates and dynamic generation
- Notifications: progress updates and status changes
- Streaming: real-time batch processing with progress

Run it:

```bash
cd example
go run cmd/assistant/main.go --http-port 8080
```

### Integration Tests

Comprehensive test suite (`integration_tests/`):

- **Protocol tests**: Initialize, capabilities, error handling
- **Tools tests**: Discovery, invocation, validation, streaming
- **Resources tests**: Discovery, reading, URI templates, subscriptions
- **Prompts tests**: Static and dynamic prompts, variables
- **Notifications tests**: Progress, status, resource changes

## 🚀 Getting Started

### Installation

```bash
go get goa.design/goa-ai
```

### Quick Start

1. **Define your service**:

```go
// design/design.go
package design

import (
    . "goa.design/goa/v3/dsl"
    mcp "goa.design/goa-ai/dsl"
)

var _ = Service("assistant", func() {
    mcp.MCPServer("assistant-mcp", "1.0.0")
    JSONRPC(func() { POST("/rpc") })
    
    Method("analyze", func() {
        Payload(String, "Text to analyze")
        Result(String)
        mcp.Tool("analyze_text", "Analyzes text")
        JSONRPC(func() {})
    })
})
```

2. **Generate the code**:

```bash
goa gen your.module/design
```

3. **Implement your service**:

```go
type assistantService struct{}

func (s *assistantService) Analyze(ctx context.Context, text string) (string, error) {
    // Your business logic here
    return "Analysis result", nil
}
```

4. **Run your server**:

```bash
goa example your.module/design
go run cmd/assistant/main.go
```

### Example Usage

Initialize connection:

```bash
curl localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
      "protocolVersion": "2025-06-18",
      "capabilities": {"tools": true}
    }
  }'
```

List available tools:

```bash
curl localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'
```

Call a tool:

```bash
curl localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {
      "name": "analyze_text",
      "arguments": {"text": "Hello world"}
    }
  }'
```

## 🎨 Design Philosophy

Goa-AI follows these core principles:

1. **Design-First**: Your DSL is the single source of truth—everything derives from it
2. **Compose, Don't Fork**: Built on top of Goa's generators, not replacing them
3. **Type Safety**: Leverage Go's type system for correctness
4. **Contract-Driven**: Trust Goa validations; avoid defensive programming
5. **Minimal Surface Area**: Small, focused API that composes well
6. **Production-Ready**: Generated code is robust, efficient, and maintainable

## 📚 Documentation

- **README.md**: Quick overview and quickstart
- **DESIGN.md**: Deep dive into architecture and implementation
- **AGENTS.md**: Repository guidelines and coding standards
- **integration_tests/README.md**: Comprehensive testing guide
- **Example service**: Complete working demonstration

## 🔧 Configuration & Customization

### Adapter Options

```go
adapter, err := mcpassistant.NewMCPAdapter(
    endpoints,
    mcpassistant.WithLogger(func(ctx context.Context, event string, details any) {
        log.Printf("[MCP] %s: %v", event, details)
    }),
    mcpassistant.WithErrorMapper(func(err error) error {
        // Map domain errors to JSON-RPC error codes
        return err
    }),
    mcpassistant.WithAllowedResourceURIs([]string{"resource://docs/*"}),
    mcpassistant.WithStructuredStreamJSON(true),
)
```

### Protocol Version

```go
mcp.MCPServer("assistant-mcp", "1.0.0", 
    mcp.ProtocolVersion("2025-06-18"))
```

### Resource Access Control

```go
adapter, err := mcpassistant.NewMCPAdapter(
    endpoints,
    mcpassistant.WithAllowedResourceURIs([]string{
        "resource://docs/*",
        "resource://config/*",
    }),
    mcpassistant.WithDeniedResourceURIs([]string{
        "resource://secrets/*",
    }),
)
```

## 🧪 Testing

Run the full test suite:

```bash
# Unit tests
make test

# Integration tests
make itest

# All tests with coverage
make ci
```

Run specific integration test categories:

```bash
cd integration_tests
go test -v ./tests -run TestMCPProtocol
go test -v ./tests -run TestMCPTools
go test -v ./tests -run TestMCPResources
go test -v ./tests -run TestMCPPrompts
```

## 🏗️ Architecture Highlights

### Code Generation Strategy

- Reuses Goa's battle-tested generators
- Filters HTTP generation for MCP-only services (JSON-RPC only)
- Transforms paths and imports to match MCP layout
- Generates MCP adapter and client wrapper
- Emits protocol version constants

### Streaming Support

- Leverages Goa's JSON-RPC SSE implementation
- No custom streaming templates needed
- Automatic SSE when methods have `StreamingResult`
- Optional structured JSON for stream events

### Schema Generation

- Derives JSON schemas from Goa types
- Supports primitives, arrays, objects, enums
- Handles required fields and validation
- Base64-encoded bytes support

## 📋 Requirements

- **Go**: 1.24 or newer
- **Goa**: v3.22.2 or newer
- **Protoc**: For running `make tools` (optional)

## 🛣️ Roadmap

Future enhancements under consideration:

- **Sampling**: Client LLM sampling requests
- **Roots**: Filesystem/URI root discovery  
- **Logging**: Structured logging protocol
- **Completion**: Autocomplete support for tool arguments
- **gRPC transport**: Alternative to JSON-RPC
- **OpenAPI integration**: Export tool definitions to OpenAPI
- **Enhanced schema validation**: More comprehensive JSON schema generation

## 🤝 Contributing

We welcome contributions! Please:

1. File issues for bugs or feature requests
2. Include failing tests or minimal designs to reproduce issues
3. Follow the coding guidelines in `AGENTS.md`
4. Ensure tests pass: `make ci`
5. Update documentation for new features

## 📄 License

MIT License—same as the Goa framework.

## 🙏 Acknowledgments

Built with and for:

- **[Goa](https://goa.design)**: The powerful design-first microservices framework
- **[Model Context Protocol](https://modelcontextprotocol.io)**: The open protocol for AI-backend communication
- The Go community for excellent tooling and libraries

## 🔗 Links

- **GitHub**: https://github.com/goadesign/goa-ai
- **Documentation**: https://pkg.go.dev/goa.design/goa-ai
- **Goa Framework**: https://goa.design
- **MCP Specification**: https://modelcontextprotocol.io/specification

---

**Ready to build your AI backend the right way?**

```bash
go get goa.design/goa-ai
```

Start with a simple design, let Goa-AI handle the rest. 🚀

