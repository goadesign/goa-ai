# Goa-AI: Design-First Agentic Systems

Build intelligent agents, MCP servers, and registry-integrated toolsets from your Goa designs. This plugin extends Goa with agent orchestration, MCP protocol support, centralized registries, and A2A cross-platform communication.

## What you get

- **Agents**: Durable plan/execute loops with policy enforcement, memory, and streaming
- **MCP**: Endpoints mapped from your Goa service (tools, resources, prompts) with JSON-RPC/SSE transport
- **Registries**: Centralized tool catalogs with federation, caching, and semantic search
- **A2A Protocol**: Cross-platform agent discovery and invocation via A2A agent cards
- **Unified Toolsets**: Single `Toolset` construct with providers (local, MCP, registry)

## How it works

For each service annotated with agents or MCP, the plugin:

1. Derives service expressions from your DSL (see `expr/agent/` and `expr/mcp.go`).
2. Runs standard Goa generators:
   - Service layer via `codegen/service` (service, endpoints, client)
   - JSON-RPC transport via `jsonrpc/codegen` (server, client, types; SSE when streaming)
   - Agent workflows, activities, and tool specs via `codegen/agent`
   - Registry clients and A2A cards via `codegen/agent`
3. Applies small, deterministic transformations so files land under appropriate paths.

We compose on top of Goa—no forks, minimal templates, and predictable output.

## Layout

- Agent packages: `gen/<svc>/agents/<agent>/`
- Tool specs: `gen/<svc>/agents/<agent>/specs/`
- MCP service: `gen/mcp_<service>/`
- A2A service: `gen/a2a_<agent>/`
- Registry clients: `gen/<svc>/registry/<name>/`

## Unified Toolset Model

Goa-AI provides a unified `Toolset` construct with configurable providers:

```go
// Local toolset (inline schemas)
var LocalTools = Toolset("utils", func() {
    Tool("summarize", "Summarize text", func() {
        Args(func() { Attribute("text", String) })
        Return(func() { Attribute("summary", String) })
    })
})

// MCP-backed toolset
var MCPTools = Toolset("assistant", FromMCP("assistant-service", "assistant-mcp"))

// Registry-backed toolset (discovered at runtime)
var RegistryTools = Toolset("enterprise", FromRegistry(CorpRegistry, "data-tools"))

// A2A-backed toolset (remote A2A provider)
var A2ATools = Toolset(FromA2A("svc.agent.tools", "https://provider.example.com"))
```

All toolsets are first-class citizens—agents use `Use(toolset)` uniformly regardless of provider.

## Registry Integration

Declare centralized registries for tool discovery and agent publication:

```go
var CorpRegistry = Registry("corp-registry", func() {
    Description("Corporate tool registry")
    URL("https://registry.corp.internal")
    APIVersion("v1")
    Security(CorpAPIKey)
    SyncInterval("5m")
    CacheTTL("1h")
})

// Federated external registry
var AnthropicRegistry = Registry("anthropic", func() {
    URL("https://registry.anthropic.com/v1")
    Security(AnthropicOAuth)
    Federation(func() {
        Include("web-search", "code-execution")
        Exclude("experimental/*")
    })
})
```

### Runtime Components

- **Registry Manager** (`runtime/registry/manager.go`): Multi-source catalog merging
- **Schema Cache** (`runtime/registry/cache.go`): TTL-based caching with fallback
- **Federation Sync**: Periodic catalog synchronization from external registries
- **Search**: Semantic and keyword-based tool discovery

## A2A Protocol Support

Agents with `Export` and `PublishTo` automatically generate A2A-compliant artifacts:

### Generated A2A Artifacts

- **Agent Card**: Protocol version, name, description, URL, capabilities, skills, security schemes
- **A2A Service**: Goa service with A2A JSON-RPC methods (`tasks/send`, `tasks/sendSubscribe`)
- **A2A Adapter**: Maps A2A tasks to agent runtime

### A2A Service Pattern

The A2A service follows the same pattern as MCP server support:

| Aspect | MCP Server | A2A Agent |
|--------|-----------|-----------|
| Protocol | MCP JSON-RPC | A2A JSON-RPC |
| Methods | `tools/call`, `resources/read`, etc. | `tasks/send`, `tasks/sendSubscribe`, etc. |
| Routes to | Service methods (via adapter) | Agent runtime (via `runtime.MustClient`) |
| Streaming | SSE for `tools/call` | SSE for `tasks/sendSubscribe` |
| Codegen | `codegen/mcp/` | `codegen/agent/a2a_*.go` |

## MCP Server Definition

Enable MCP protocol for a service with `MCP`:

```go
Service("calculator", func() {
    MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
    Method("add", func() {
        Payload(func() { Attribute("a", Int); Attribute("b", Int) })
        Result(func() { Attribute("sum", Int) })
        Tool("add", "Add two numbers") // Context-aware in Method
    })
})
```

### Protocol version

Set the MCP protocol version in your design using the DSL option on `MCP`:

```go
MCP("assistant-mcp", "1.0.0", ProtocolVersion("2025-06-18"))
```

The generator emits a constant `DefaultProtocolVersion` in `gen/mcp_<service>/protocol_version.go`.

### Adapter options

The generated `MCPAdapterOptions` provides configuration hooks:

- Logger: `func(ctx context.Context, event string, details any)` to observe adapter lifecycle.
- ErrorMapper: `func(error) error` to normalize errors to JSON-RPC codes.
- AllowedResourceURIs, DeniedResourceURIs: simple allow/deny lists for resource URIs.
- StructuredStreamJSON: when true, stream events are emitted as `resource` items with `application/json`.
- ProtocolVersionOverride: override `DefaultProtocolVersion` at construction time.

## Streaming

No custom streaming templates. When your methods stream, Goa's JSON-RPC generator emits the SSE stack. We simply adjust paths/imports so it lives under the MCP tree.

## Tool Input Schema

For each tool with a non-empty payload, the plugin derives a compact JSON Schema from the Goa attribute and exposes it in `tools/list` under `inputSchema`. This uses Goa's `openapi.Schema` type for complete JSON Schema draft 2020-12 support.

## Tool Identification

Tools are identified by canonical IDs in the format `<toolset>.<tool>` (dot-separated). The generated code produces typed constants (e.g., `MyTool tools.Ident`) matching this format.

## Agents Quickstart & Example Scaffold

A contextual quickstart file `AGENTS_QUICKSTART.md` is emitted at the module root on `goa gen`, summarizing what was generated and how to wire it. To opt out, invoke `DisableAgentDocs()` inside your API DSL.

The `goa example` phase generates application-owned scaffold under `internal/agents/`:

- `internal/agents/bootstrap/bootstrap.go`: constructs a minimal runtime and registers generated agents
- `internal/agents/<agent>/planner/planner.go`: planner stub implementing `PlanStart`/`PlanResume`
- `internal/agents/<agent>/toolsets/<toolset>/adapter.go`: stubs for mapping method-backed tools

## Security considerations

- Resource policy: use deny/allow lists to constrain which URIs can be read
- Registry authentication: use Goa security schemes (`APIKeySecurity`, `OAuth2Security`, etc.)
- A2A security: agent cards include security schemes for cross-platform authentication
- Logging: avoid logging sensitive payloads and results in production

## Error code mapping

The adapter maps Goa `ServiceError` with name `invalid_params` to JSON-RPC `-32602`, `method_not_found` to `-32601`, and otherwise defaults to `-32603` (internal).

## Contributing

- Add agent concepts in `expr/agent/` and update the expression builders
- Add MCP concepts in `expr/mcp.go` and update the MCP expression builder
- Add registry concepts in `expr/agent/registry.go`
- Keep new templates small and transport-agnostic; compose on Goa JSON-RPC outputs

## Summary

This plugin gives you agents, MCP, registries, and A2A with familiar Goa patterns, minimal surface area, and a directory layout that feels natural. It's accurate, easy to maintain, and designed to evolve alongside Goa.
