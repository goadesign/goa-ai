# Goa MCP Plugin

Design-first Model Context Protocol servers generated from your Goa service design.

Why Goa + MCP
- One design, many surfaces. Describe your service once in Goa’s DSL and generate a fully-typed MCP server that tools, prompts, and resources can call. No drift between docs, types, handlers, and clients.
- Strong types → clear schemas. The plugin derives compact JSON Schema for tool inputs from your Goa payloads, so models get reliable shapes and you keep compile-time confidence.
- Built-in streaming done right. JSON‑RPC over HTTP with Server‑Sent Events (SSE) is negotiated by the Accept header — non‑streaming and streaming endpoints coexist cleanly.
- Opinionated protocol glue. Initialization gating, capability negotiation, and consistent JSON‑RPC error mapping are handled for you so you can focus on domain logic.
- Batteries included. Generated adapters, clients, and an example server make it easy to try, test, and ship. A comprehensive integration test suite ships in this repo.

What You Get
- Tools: expose methods as MCP tools callable by AI agents.
- Resources: serve contextual data via URI-like addresses (list, read, subscribe).
- Prompts: define static templates or generate dynamic prompts from code.
- Notifications and subscriptions: send progress, status, and change events.
- Transports: JSON‑RPC over HTTP with optional SSE for streaming.

Quickstart
1) Install
   - `go get goa.design/plugins/v3/mcp`

2) Import the MCP DSL in your design
   - `import mcp "goa.design/plugins/v3/mcp/dsl"`

3) Enable MCP and JSON‑RPC on a service
   - Minimal example:
     ```go
     var _ = Service("assistant", func() {
       Description("MCP-enabled assistant service")
       mcp.MCPServer("assistant-mcp", "1.0.0")
       JSONRPC(func() { POST("/rpc") })
     })
     ```

4) Expose functionality
   - Tool:
     ```go
     Method("analyze", func() {
       Payload(func() { Attribute("text", String); Required("text") })
       Result(String)
       mcp.Tool("analyze_text", "Analyze user text")
       JSONRPC(func() {})
     })
     ```
   - Resource:
     ```go
     Method("systemInfo", func() {
       Result(MapOf(String, String))
       mcp.Resource("system_info", "system://info", "application/json")
       JSONRPC(func() {})
     })
     ```
   - Dynamic prompt:
     ```go
     Method("makePrompt", func() {
       Payload(func(){ Attribute("topic", String); Required("topic") })
       Result(ArrayOf(PromptResult))
       mcp.DynamicPrompt("topic_prompts", "Generate prompts for a topic")
       JSONRPC(func() {})
     })
     ```

5) Add streaming (optional)
   - Mixed mode (HTTP or SSE by Accept header):
     ```go
     Method("processBatch", func() {
       Payload(func(){ Attribute("items", ArrayOf(String)); Required("items") })
       Result(String)
       StreamingResult(String)
       mcp.Tool("process_batch", "Process items with progress updates")
       JSONRPC(func() {})
     })
     ```

6) Generate and run
   - `goa gen <module>/design`
   - `goa example <module>/design`
   - `go run cmd/<service>/main.go --http-port 8080`

Try It (curl)
- Initialize (once per connection):
  ```bash
  curl -s localhost:8080/rpc -H 'Content-Type: application/json' -d '{
    "jsonrpc":"2.0","id":1,"method":"initialize",
    "params":{"protocolVersion":"2025-06-18","capabilities":{"tools":true,"resources":true,"prompts":true}}
  }'
  ```

- List tools:
  ```bash
  curl -s localhost:8080/rpc -H 'Content-Type: application/json' -d '{
    "jsonrpc":"2.0","id":2,"method":"tools/list","params":{}
  }' | jq
  ```

- Call a streaming tool (SSE):
  ```bash
  curl -N localhost:8080/rpc \
    -H 'Accept: text/event-stream' -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"process_batch","arguments":{"items":["a","b"]}}}'
  ```

Concepts & Mapping
- Tools → Goa Method with `mcp.Tool(...)` and `JSONRPC(func(){})`.
- Resources → Goa Method with `mcp.Resource(...)` and `JSONRPC(func(){})`.
- Static prompts → `mcp.StaticPrompt(...)` at service scope.
- Dynamic prompts → Goa Method with `mcp.DynamicPrompt(...)` and `JSONRPC(func(){})`.
- Streaming → add `StreamingResult(...)` (optional alongside `Result(...)`).
- Content negotiation → set `Accept: text/event-stream` for streaming.

Initialization & Errors
- Calls are gated until `initialize` succeeds.
- JSON‑RPC error mapping used by the generated server:
  - `-32601` Method Not Found for unknown tools/resources/prompts.
  - `-32602` Invalid Params for decode/validation/service param errors.
  - `-32603` Internal Error for unhandled failures.
- SSE errors are emitted as `event: error` with a JSON‑RPC error body.

Why This Approach Works
- Design-first development reduces surface bugs: one source of truth for types, transports, and documentation.
- Automatic schema and adapter generation accelerates iteration and improves agent reliability.
- Unified transport with first-class SSE encourages gradual adoption of streaming without separate stacks.
- Integration tests as contract: this repo ships high-signal scenarios you can extend or mirror.

Integration Tests
- Location: `integration_tests/`
- Run everything:
  - `go test -v ./integration_tests/tests`
- Useful env vars:
  - `TEST_PARALLEL=true` run scenarios in parallel
  - `TEST_FILTER=initialize.*` filter by scenario name
  - `TEST_KEEP_GENERATED=true` keep generated example code for debugging
  - `TEST_DEBUG=true` verbose runner logs

Requirements
- Goa v3.22.2 or newer (JSON‑RPC mixed transport and SSE fixes).
- Go 1.24+.

Recipes
- Resource URI params are coerced into your payload types: repeated params → arrays, booleans/ints/floats parsed when possible.
- Mixed response methods: declare both `Result(...)` and `StreamingResult(...)` to let clients choose HTTP or SSE via Accept.
- SSE event framing: interim updates use JSON‑RPC notifications, final response uses a JSON‑RPC success envelope.

FAQ
- Do I need WebSockets? No — JSON‑RPC over HTTP + SSE covers request/response and server‑push efficiently.
- Can I keep my existing Goa service? Yes. The plugin generates an MCP adapter that wraps your service; you keep your domain code.
- How do I test complex outputs? Extend or add scenarios in `integration_tests/scenarios/*.yaml` and use partial matching in expectations.

Contributing
- Issues and PRs welcome. Please include a failing scenario or a minimal design when possible.

License
MIT — same as the Goa framework.
