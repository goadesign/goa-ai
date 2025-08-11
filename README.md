# Goa MCP Plugin

A Goa plugin that enables Model Context Protocol (MCP) server generation from Goa service designs.

## Overview

This plugin extends Goa's DSL to allow services to be exposed as MCP servers, providing:
- **Tools**: Expose service methods as callable tools for AI models
- **Resources**: Provide contextual data through service endpoints
- **Prompts**: Define templated prompts for AI interactions
- **Dynamic Prompts**: Generate prompts dynamically based on runtime data

The plugin leverages Goa's native JSON-RPC support to implement the MCP protocol with automatic SSE streaming when clients send `Accept: text/event-stream` header.

## Installation

```bash
go get goa.design/plugins/v3/mcp
```

## Usage

### 1. Import the Plugin DSL

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/plugins/v3/mcp/dsl"
)
```

### 2. Define an MCP Server

```go
var _ = Service("calculator", func() {
    Description("Calculator service")
    
    // Configure MCP server
    // Transport is automatically JSON-RPC
    // Capabilities are auto-detected from defined tools, resources, etc.
    MCPServer("calculator-mcp", "1.0.0")
    
    // Enable JSON-RPC (automatically supports SSE via Accept header)
    JSONRPC(func() {
        POST("/rpc")
    })
})
```

### 3. Define Tools

Expose service methods as MCP tools:

```go
Method("add", func() {
    Description("Add two numbers")
    Payload(func() {
        Attribute("a", Float64, "First number")
        Attribute("b", Float64, "Second number")
        Required("a", "b")
    })
    Result(Float64)
    
    // Mark as MCP tool
    Tool("add", "Add two numbers together")
    
    JSONRPC(func() {})
})
```

### 4. Define Resources

Expose data as MCP resources:

```go
Method("history", func() {
    Description("Get calculation history")
    Result(ArrayOf(HistoryEntry))
    
    // Mark as MCP resource
    Resource("calculation_history", "history://calculations", "application/json")
    
    JSONRPC(func() {})
})
```

### 5. Define Prompts

Create templated prompts for AI interactions:

```go
// Define static prompts
StaticPrompt(
    "solve_equation",
    "Prompt for solving equations",
    "system", "You are a mathematical assistant.",
    "user", "Please solve: {{.equation}}",
    "assistant", "I'll solve this step by step.",
)
```

### 6. Dynamic Prompts

Generate prompts dynamically:

```go
Method("generate_prompts", func() {
    Description("Generate prompts dynamically")
    Payload(func() {
        Attribute("topic", String, "Topic for prompts")
        Required("topic")
    })
    Result(ArrayOf(PromptResult))
    
    // Mark as dynamic prompt generator
    DynamicPrompt("calculation_prompts", "Generate calculation prompts")
    
    JSONRPC(func() {})
})
```

## Code Generation

Generate the MCP server code:

```bash
goa gen <module>/design
```

This generates:
- MCP server implementation in `gen/mcp/`
- Transport adapters (stdio, HTTP, WebSocket)
- Client implementation for testing
- Example main file in `cmd/<service>_mcp/`

## Running the Server

### JSON-RPC Server

```bash
go run cmd/calculator/main.go --http-port 8080
```

The server automatically:
- Handles MCP protocol methods at `/rpc`
- Supports SSE streaming when clients send `Accept: text/event-stream`
- Provides both HTTP and JSON-RPC endpoints

## Integration with AI Models

The generated MCP server can be integrated with:
- Claude Desktop (via MCP SDK)
- Other AI assistants supporting MCP
- Custom AI applications using the MCP client

### Example MCP Client Configuration

```json
{
  "servers": {
    "calculator": {
      "url": "http://localhost:8080/rpc",
      "transport": "jsonrpc"
    }
  }
}
```

## Security

The plugin integrates with Goa's security DSL:

```go
Service("secure", func() {
    // Define security
    Security(BasicAuth, func() {
        Scope("api:read", "Read access")
        Scope("api:write", "Write access")
    })
    
    MCPServer(func() {
        // MCP configuration
    })
    
    Method("protected", func() {
        Security(BasicAuth, func() {
            Scope("api:write")
        })
        // Method definition
    })
})
```

## Architecture

The plugin follows Goa's architecture:

1. **DSL Layer** (`dsl/`): Extends Goa's DSL with MCP-specific functions
2. **Expression Layer** (`expr/`): Defines data structures for MCP metadata
3. **Code Generation** (`codegen/`): Generates type-safe MCP server code
4. **Transport Adapters**: Implements MCP protocol over different transports

The generated code:
- Wraps service implementations with MCP protocol handling
- Provides type-safe interfaces for tools, resources, and prompts
- Handles JSON-RPC communication and error handling
- Supports capability negotiation and session management

## Supported MCP Features

The plugin supports all features from the MCP 2025-06-18 specification:

### Server Features
- **Tools**: Expose service methods as AI-callable tools
- **Resources**: Provide data resources with URI templates
- **Prompts**: Define static and dynamic prompt templates

### Client Features  
- **Sampling**: Request LLM completions from the client
- **Roots**: Query filesystem/URI roots from the client
- **Elicitation**: Request additional information from users

### Additional Protocol Features
- **Notifications**: Send status updates to clients
- **Progress Tracking**: Report progress for long operations
- **Cancellation**: Support for canceling operations
- **Logging**: Structured logging support
- **Completion**: Auto-completion support
- **Subscriptions**: Resource update subscriptions
- **Server-Sent Events (SSE)**: Stream real-time updates when clients set `Accept: text/event-stream` header
  - Resource subscription monitoring
  - Log streaming  
  - Real-time updates

### Technical Features
- ✅ Type-safe code generation
- ✅ Integration with Goa's validation and error handling
- ✅ Security integration via Goa's security DSL
- ✅ Automatic capability detection
- ✅ JSON-RPC transport with SSE support

## Contributing

Contributions are welcome! Please ensure:
- Code follows Goa's coding standards
- Tests are included for new features
- Documentation is updated

## License

Same as Goa framework