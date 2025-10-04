<p align="center">
  <p align="center">
    <a href="https://goa.design">
      <img alt="Goa-AI" src="https://raw.githubusercontent.com/goadesign/goa-ai/main/docs/img/goa-ai-banner.png">
    </a>
  </p>
  <p align="center">
    <a href="https://github.com/goadesign/goa-ai/releases/latest"><img alt="Release" src="https://img.shields.io/github/release/goadesign/goa-ai.svg?style=for-the-badge"></a>
    <a href="https://pkg.go.dev/goa.design/goa-ai"><img alt="Go Doc" src="https://img.shields.io/badge/godoc-reference-blue.svg?style=for-the-badge"></a>
    <a href="https://github.com/goadesign/goa-ai/actions/workflows/ci.yml"><img alt="GitHub Action: CI" src="https://img.shields.io/github/actions/workflow/status/goadesign/goa-ai/ci.yml?branch=main&style=for-the-badge"></a>
    <a href="https://goreportcard.com/report/goa.design/goa-ai"><img alt="Go Report Card" src="https://goreportcard.com/badge/goa.design/goa-ai?style=for-the-badge"></a>
    <a href="/LICENSE"><img alt="Software License" src="https://img.shields.io/badge/license-MIT-brightgreen.svg?style=for-the-badge"></a>
  </p>
</p>

# Goa-AI: The Design-First Toolkit for AI Backends

**Stop handwriting API specs and boilerplate for your AI agents. Start describing your tools in simple, type-safe Go and let `goa-ai` generate the entire robust, streaming-capable backend for you.**

Building reliable backends for AI agents is a new kind of challenge. You're constantly fighting to keep your agent's tool definitions, your API's JSON schema, and your actual backend implementation in sync. This constant manual translation is slow, error-prone, and full of tedious boilerplate.

`Goa-AI` solves this. It introduces a design-first methodology that allows you to go from a simple Go definition to a production-ready AI backend in minutes, not days.

### The Design-First Advantage: From DSL to Live Server

See how a simple, readable design becomes a powerful, feature-rich server with a single command. This is the core of the `goa-ai` workflow.

<table>
  <thead>
    <tr>
      <th align="left">You Write This... (A Simple Go DSL)</th>
      <th align="left">...And You Get This (A Production-Ready AI Backend)</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td valign="top">

<pre><code>// design/design.go
var _ = Service("orders", func() {
  // Describe a tool for your AI agent
  Method("get_status", func() {
    Payload(String, "Order ID")
    Result(OrderStatus)

    // Decorate it for the AI
    mcp.Tool(
      "lookup_order_status",
      "Gets the current status of an order.",
    )
  })
})
</code></pre>

      </td>
      <td valign="top">

<ul>
  <li>Strongly-typed JSON Schema for the model.</li>
  <li>Boilerplate-free server handlers.</li>
  <li>JSON-RPC transport over HTTP.</li>
  <li>First-class streaming via SSE.</li>
  <li>Automatic error mapping.</li>
  <li>Built-in capability negotiation.</li>
  <li>...and much more, generated instantly by running:</li>
</ul>

<pre><code>goa gen my-module/design
</code></pre>

      </td>
    </tr>
  </tbody>
  
</table>

You remain focused on your business logic. `goa-ai` handles the complex, tedious protocol and transport layers.

## Why This is a Better Way to Build for AI

  * **Eliminate Drift and Hallucinations**: By generating the server, client, and JSON Schema from a single source of truth, you make it impossible for your agent's tools to become outdated. This drastically reduces model errors and failed API calls.
  * **Give Your Agent a Voice with Streaming**: Easily add a `StreamingResult` to your design to push real-time progress updates. Your agent can stream responses like "Searching for flights...", "Analyzing the data...", and "Finalizing the report..." without any complex transport logic on your part.
  * **Type-Safety Meets AI**: Leverage Go's powerful type system to define your tools. `goa-ai` ensures that the data structures you define in Go are the same ones the language model uses, backed by compile-time checks.
  * **Focus on What Matters**: Stop wasting time on API boilerplate—serialization, routing, validation, error handling. The generated code is robust, efficient, and lets you concentrate entirely on your application's core functionality.

## The Technology Behind `goa-ai`

Now that you've seen the "why," here's the "how." `goa-ai` is powered by two key technologies:

  * **[Goa](https://goa.design)**: A powerful framework for building micro-services in Go using a design-first approach. You write a simple DSL in Go to describe your service's API, and Goa uses that to generate code, documentation, and more. It combines the rigor of OpenAPI or gRPC with the expressiveness of pure Go.

  * **[MCP (Model Context Protocol)](https://www.google.com/search?q=https://github.com/model-context/protocol)**: An open, opinionated protocol designed specifically for communication between language models and backend systems. It standardizes how models discover and call tools, access data resources, and receive streaming updates.

`goa-ai` seamlessly bridges the two, making Goa the fastest and most reliable way to build a production-grade MCP server.

## Quickstart

Get a simple MCP server running in just a few steps.

### 1\. Install the Toolkit

```bash
go get goa.design/goa-ai
```

### 2\. Define a Service in a `design` folder

Create a `design/design.go` file and describe your service.

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    mcp "goa.design/goa-ai/dsl"
)

var _ = Service("assistant", func() {
	Description("An MCP-enabled assistant service")

	// Enable MCP for this service.
	mcp.MCPServer("assistant-mcp", "1.0.0")

	// Expose it over the JSON-RPC transport.
	JSONRPC(func() {
		POST("/rpc")
	})

	// Expose a method as an AI tool.
	Method("analyze", func() {
		Payload(String, "Text to analyze")
		Result(String)
		mcp.Tool("analyze_text", "Analyzes user-provided text")
		JSONRPC(func() {})
	})
})
```

### 3\. Generate and Run

```bash
# Generate the server code
goa gen your.module/design

# (Optional) Generate and run the example server
goa example your.module/design
go run cmd/assistant/main.go --http-port 8080
```

Your AI-ready backend is now live.

## Usage Examples

Interact with your running server using `curl`.

#### 1\. Initialize the Connection

```bash
curl -s localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0", "id": 1, "method": "initialize",
    "params": {"protocolVersion": "2025-06-18", "capabilities": {"tools": true}}
  }'
```

#### 2\. List Available Tools

```bash
curl -s localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}' | jq
```

## Requirements

  * **Go**: `1.24` or newer
  * **Goa**: `v3.22.2` or newer

## Contributing

Issues and Pull Requests are welcome. When reporting a bug or proposing a change, please include a failing test scenario or a minimal Goa design to reproduce the issue.

## License

**MIT**—same as the Goa framework.