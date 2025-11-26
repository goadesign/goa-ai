# Goa-AI v0.9.0 — The Agent Framework Release

**The design-first agent framework for Go is here.**

This release transforms Goa-AI from an MCP plugin into a complete framework for building **production-grade agentic systems**. Define agents, toolsets, and policies in Goa's elegant DSL. Generate typed code, durable workflows, and runtime integrations. Ship agents that survive restarts, scale with Temporal, and integrate seamlessly with your existing Goa services.

📚 **[Full Documentation](https://goa.design/docs/8-goa-ai/)** · 🚀 **[Getting Started](https://goa.design/docs/8-goa-ai/1-getting-started/)** · 💡 **[Tutorials](https://goa.design/docs/8-goa-ai/3-tutorials/)**

---

## Highlights

### 🔗 Zero-Glue Tooling with `BindTo`

Turn any Goa service method into an LLM tool with a single line:

```go
Tool("search_docs", "Search documents", func() {
    BindTo("search")           // Bind to service method
    Inject("session_id")       // Hide infrastructure from LLM
})
```

Codegen handles the type mapping. Your validated service logic becomes instantly available to agents.

### 🏗️ Durable Execution with Temporal

Agents crash. Networks fail. Goa-AI doesn't care.

- **Deterministic replay**: Resume exactly where you left off
- **Automatic retries**: Configurable per-tool and per-turn
- **Time budgets**: Never let a runaway agent burn tokens forever
- **In-memory for dev, Temporal for prod**: Same code, swap the engine

### 🤖 Agent-as-Tool Composition

Build hierarchies of specialist agents:

```go
Agent("data-analyst", "Expert at queries", func() {
    Export("analysis", func() {
        Tool("analyze", "Deep analysis", func() { ... })
    })
})

Agent("orchestrator", "Main chat", func() {
    Use(AgentToolset("svc", "data-analyst", "analysis"))
})
```

Nested agents run inline—single transaction, unified history, no network hops.

### 🔌 Universal MCP Adapter

Goa-AI is a two-way MCP bridge:

- **Consume**: Import any MCP server as a typed toolset
- **Serve**: Expose your Goa services as MCP servers automatically

Write once, run as an agent tool, run as an MCP server—simultaneously.

### 🔍 Debuggable by Design

- **Transcript Ledger**: Every thinking block, tool call, and result in exact provider order
- **Distributed Tracing**: Trace decisions from Thinking → Tool Call → Result → Final Answer
- **Structured Streaming**: Real-time events for UIs via Pulse/WebSocket/SSE

---

## What's New

### DSL

- **`Agent`**: Declare agents with planners, toolsets, and policies
- **`Toolset`**: Group related tools with type-safe schemas
- **`BindTo`**: Bind tools to service methods with auto-generated transforms
- **`Inject`**: Hide infrastructure fields from LLMs
- **`Export` / `Use`**: Compose agents as tools
- **`RunPolicy`**: Set caps, time budgets, and failure limits
- **`MCPServer` / `MCPTool`**: Expose services as MCP servers

### Codegen

- **Agent packages**: Workflow definitions, planner activities, registration helpers
- **Tool specs**: Typed payload/result structs, JSON codecs, JSON Schema
- **Transforms**: Auto-generated mappers between tool and service types
- **`AGENTS_QUICKSTART.md`**: Contextual guide generated per project

### Runtime

- **Plan/Execute loop**: Durable orchestration with policy enforcement
- **Transcript ledger**: Provider-precise message history for replay
- **Session/Run stores**: MongoDB-backed persistence
- **Stream hooks**: Pulse sink for real-time UI events
- **Model clients**: Bedrock, OpenAI, Gateway adapters
- **Human-in-the-loop**: Pause/resume with interrupt controller

### MCP

- **Retryable errors**: `RetryableError` with repair prompts for LLM-driven recovery
- **Typed JSON-RPC errors**: Proper `-32602` / `-32601` mapping
- **CLI test mode**: Integration testing with `go run` of example CLIs
- **SSE streaming**: Accept-based content negotiation

---

## Breaking Changes

- Module path changed to `goa.design/goa-ai`
- MCP adapter generation refactored for stable package names
- Example CLI wiring now goes through MCP adapters

---

## Migration

1. Update imports: `goa.design/goa-ai`
2. Regenerate: `goa gen ./design`
3. Review `AGENTS_QUICKSTART.md` for project-specific guidance

---

## Requirements

- Go 1.24+
- Goa v3.22.2+
- Temporal SDK v1.37.0 (for durable execution)
- MongoDB & Redis/Pulse (optional, for persistence and streaming)

---

## Thanks

To everyone who provided feedback, tested early builds, and helped shape the vision. This release is the foundation for building agents that are a joy to develop and a breeze to operate.

**Welcome to Goa-AI.**

