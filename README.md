<p align="center">
  <p align="center">
    <a href="https://goa.design">
      <img alt="Goa-AI" src="https://raw.githubusercontent.com/goadesign/goa-ai/main/docs/img/goa-ai-banner.png" width="50%">
    </a>
  </p>
  <p align="center">
    <a href="https://github.com/goadesign/goa-ai/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/goadesign/goa-ai?style=for-the-badge"></a>
    <a href="https://pkg.go.dev/goa.design/goa-ai"><img alt="Go Doc" src="https://img.shields.io/badge/godoc-reference-blue.svg?style=for-the-badge"></a>
    <a href="https://github.com/goadesign/goa-ai/actions/workflows/ci.yml"><img alt="GitHub Action: CI" src="https://img.shields.io/github/actions/workflow/status/goadesign/goa-ai/ci.yml?branch=main&style=for-the-badge"></a>
    <a href="https://goreportcard.com/report/goa.design/goa-ai"><img alt="Go Report Card" src="https://goreportcard.com/badge/goa.design/goa-ai?style=for-the-badge"></a>
    <a href="/LICENSE"><img alt="Software License" src="https://img.shields.io/badge/license-MIT-brightgreen.svg?style=for-the-badge"></a>
  </p>
</p>

# Goa-AI · Design-First Agent Framework

Goa-AI turns Goa's design-first workflow into a complete framework for building **agentic, tool-driven systems** in Go. Describe your agents, toolsets, policies, and workflows once in Go, then let Goa-AI generate:

- Typed tool payload/result structs plus JSON codecs (no more hand-written schemas)
- Temporal-ready workflows & activities (Plan/Resume/Execute) with retry/timeouts baked in
- Runtime registries for toolsets, exported agents, and MCP suites
- Durable execution loop with policy enforcement, memory persistence, stream hooks, and telemetry

The result is a cohesive architecture where planners focus on business logic while Goa-AI supplies the plumbing for Temporal, Mongo-backed memory, Pulse streams, MCP integration, and model providers.

> 📚 **Documentation:**
> - [Architecture & plan](docs/plan.md)
> - [Agent DSL reference](docs/dsl.md)
> - [Runtime wiring](docs/runtime.md)
> - [Migration guide (legacy goa-ai → new framework)](docs/migrate.md)
> - [AURA API types migration plan](docs/aura_api_types_migration.md)

## Quick Start

### 1. Install Goa & Goa-AI
```bash
go install goa.design/goa/v3/cmd/goa@latest
go get goa.design/goa-ai
```

### 2. Define a service + agent in `design/design.go`
```go
package design

import (
    . "goa.design/goa/v3/dsl"
    agentsdsl "goa.design/goa-ai/dsl"
)

var AtlasReadToolset = agentsdsl.Toolset("atlas.read", func() {
    Tool("find_events", "Query recent Atlas events", func() {
        Args(func() {
            Attribute("query", String, "Search expression")
            Required("query")
        })
        Return(func() {
            Attribute("summary", String, "Natural language summary")
        })
    })
})

var AssistantSuite = agentsdsl.MCPToolset("assistant", "assistant-mcp")

var _ = Service("orchestrator", func() {
    Description("Chat orchestrator")

    agentsdsl.Agent("orchestrator.chat", "LLM-driven planner", func() {
        agentsdsl.Use(AtlasReadToolset)
        agentsdsl.Use(AssistantSuite)

        agentsdsl.RunPolicy(func() {
            MaxToolCalls(8)
            TimeBudget("2m")
        })
    })
})
```

### 3. Generate code
```bash
goa gen example.com/assistant/design
```
This produces agent packages under `gen/orchestrator/agents/...`, tool codecs/specs, planner configs, and Temporal activities.

> Note: After generation, a contextual guide named `AGENTS_QUICKSTART.md` is written at the module root to summarize what was generated and how to use it. To opt out, call `agentsdsl.DisableAgentDocs()` inside your API DSL.

### 4. Wire the runtime + Temporal engine
```go
package main

import (
    "context"

    "go.temporal.io/sdk/client"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    runtimeTemporal "goa.design/goa-ai/runtime/agent/engine/temporal"
    "goa.design/goa-ai/runtime/agent/runtime"
    basicpolicy "goa.design/goa-ai/features/policy/basic"
    memorymongo "goa.design/goa-ai/features/memory/mongo"
    runmongo "goa.design/goa-ai/features/run/mongo"
    pulse "goa.design/goa-ai/features/stream/pulse"
    "goa.design/goa-ai/runtime/agent/telemetry"
)

func main() {
    temporalEng, err := runtimeTemporal.New(runtimeTemporal.Options{
        ClientOptions: &client.Options{HostPort: "127.0.0.1:7233", Namespace: "default"},
        WorkerOptions: runtimeTemporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
    })
    if err != nil {
        panic(err)
    }
    defer temporalEng.Close()

    mongoClient := newMongoClient()    // user-provided helper
    redisClient := newRedisClient()    // user-provided helper
    pulseSink, err := pulse.NewSink(pulse.Options{Client: redisClient})
    if err != nil {
        panic(err)
    }

    rt := runtime.New(runtime.Options{
        Engine:      temporalEng,
        MemoryStore: memorymongo.New(mongoClient),
        RunStore:    runmongo.New(mongoClient),
        Stream:      pulseSink,
        Policy:      basicpolicy.New(),
        Logger:      telemetry.NewClueLogger(),
        Metrics:     telemetry.NewClueMetrics(),
        Tracer:      telemetry.NewClueTracer(),
    })

    if err := chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{
        Planner:        newChatPlanner(),
        PromptProvider: chat.NewPromptProvider(),
    }); err != nil {
        panic(err)
    }

    client := chat.NewClient(rt)
    handle, err := client.Start(context.Background(),
        []model.Message{{Role: "user", Content: "Summarize the latest status"}},
        runtime.WithSessionID("session-1"),
    )
    if err != nil {
        panic(err)
    }
    var out *runtime.RunOutput
    if err := handle.Wait(context.Background(), &out); err != nil {
        panic(err)
    }
}
```

### Optional: Internal example scaffold

If you run `goa example`, the example scaffold is emitted under `internal/agents/` instead of modifying your `main` package. It includes:

- `internal/agents/bootstrap/bootstrap.go`: constructs a minimal runtime and registers generated agents. This file is application-owned; edit and maintain it (it is not re-generated).
- `internal/agents/<agent>/planner/planner.go`: planner stub to replace with your LLM logic.
- `internal/agents/<agent>/toolsets/<toolset>/execute.go`: executor stub for method‑backed tools (decode typed args, optionally use transforms, call your client, return ToolResult).

Use the bootstrap from your `cmd` entrypoint:

```go
package main

import (
    "context"
    "log"

    "example.com/assistant/internal/agents/bootstrap"
)

func main() {
    ctx := context.Background()
    rt, cleanup, err := bootstrap.New(ctx)
    if err != nil {
        log.Fatal(err)
    }
    defer cleanup()

    // Start runs or serve transports using the runtime...
}
```

### Executor‑First TL;DR

- Register method‑backed toolsets with a ToolCallExecutor you control:
  - For service/method-backed toolsets, applications construct registrations explicitly and provide an executor. Codegen supplies registration helpers; the application wires them with `rt.RegisterToolset`.
  - `rt.RegisterToolset(reg)`
- Implement `Execute(ctx, meta, call)` with a per‑tool switch:
  - Decode typed payload using generated codecs: `args, _ := <specspkg>.Unmarshal<ToolPayload>(call.Payload)`
  - Optionally use transforms from `specs/<toolset>/transforms.go` when shapes are compatible:
    - `mp, _ := <specspkg>.ToMethodPayload_<Tool>(args)`
    - `tr, _ := <specspkg>.ToToolReturn_<Tool>(methodRes)`
  - Call your service client and return `planner.ToolResult{Payload: tr}` (or map explicitly if no transform was emitted).

Minimal executor sketch:

```go
// Register at startup
// Build ToolsetRegistration explicitly in application code
if err := rt.RegisterToolset(reg); err != nil { panic(err) }

// Execute per tool
func Execute(ctx context.Context, meta runtime.ToolCallMeta, call planner.ToolRequest) (planner.ToolResult, error) {
    switch call.Name {
    case "orchestrator.profiles.upsert":
        args, err := profilesspecs.UnmarshalUpsertPayload(call.Payload)
        if err != nil { return planner.ToolResult{Error: planner.NewToolError("invalid payload")}, nil }

        // Optional transforms if emitted by codegen
        // mp, _ := profilesspecs.ToMethodPayload_Upsert(args)
        // methodRes, err := client.Upsert(ctx, mp)
        // if err != nil { return planner.ToolResult{Error: planner.ToolErrorFromError(err)}, nil }
        // tr, _ := profilesspecs.ToToolReturn_Upsert(methodRes)
        // return planner.ToolResult{Payload: tr}, nil

        // Or explicit mapping when no transform exists
        return planner.ToolResult{Payload: map[string]any{"status": "ok"}}, nil
    default:
        return planner.ToolResult{Error: planner.NewToolError("unknown tool")}, nil
    }
}
```

Transforms are emitted only when the tool Arg/Return and method Payload/Result are structurally compatible. When they are not, write the field mapping explicitly inside your executor.

### 5. Explore the example
`example/complete/` contains the **chat data loop** harness: it registers the generated MCP toolset helper, runs the runtime harness (in-process engine), and demonstrates planners invoking MCP tools with streaming + memory hooks. Run it via:
```bash
go test ./example
```

Need to hand execution back to a human reviewer? Use the built-in interrupt API to pause and resume durable runs:

```go
rt.PauseRun(ctx, interrupt.PauseRequest{
    RunID:       "session-1-run-1",
    Reason:      "human_review",
    RequestedBy: "policy-engine",
})

// ... later ...
rt.ResumeRun(ctx, interrupt.ResumeRequest{
    RunID:  "session-1-run-1",
    Notes:  "Reviewer approved",
})
```

The workflow loop drains `goaai.runtime.pause` / `goaai.runtime.resume` signals via the interrupt controller, updates the run store, and emits `run_paused` / `run_resumed` hook events so Pulse subscribers stay in sync.

## Architecture Overview

```
 DSL → Codegen → Runtime → Engine + Features
```

| Layer | Responsibility |
| --- | --- |
| **DSL (`dsl`)** | Define agents, toolsets, policies, MCP suites inside Goa services. |
| **Codegen (`codegen/agent`, `codegen/mcp`)** | Emit tool codecs/specs, registries, Temporal workflows, activity handlers, MCP helpers. |
| **Runtime (`runtime/agent`, `runtime/mcp`)** | Durable plan/execute loop with policy enforcement, memory/session stores, hook bus, telemetry, MCP callers. |
| **Engine (`runtime/agents/engine`)** | Abstract workflow API; Temporal adapter ships with OTEL interceptors, auto-start workers, and context propagation. |
| **Features (`features/*`)** | Optional modules (Mongo memory/session, Pulse stream sink, MCP callers, Bedrock/OpenAI model clients, policy engine). |

See `docs/plan.md` for a deep dive into generated structures, templates, and runtime packages.

### Toolsets: service‑backed vs agent‑exported

Goa‑AI generates slightly different helpers depending on how a toolset is declared.

- **Service‑backed toolsets (method‑backed)**
  - Declared in an agent `Uses` block with tools bound to Goa service methods.
  - Codegen emits per‑toolset specs/types/codecs, executor factories (for example,
    `New<Agent><Toolset>Exec`), and `RegisterUsedToolsets`/`With<...>Executor` helpers
    so applications can bind service clients and register toolsets.

- **Agent‑exported toolsets (agent‑as‑tool)**
  - Declared in an agent `Exports` block and optionally `Uses`d by other agents.
  - Codegen emits provider‑side `agenttools/<toolset>` helpers with `NewRegistration`
    and typed call builders, plus consumer helpers like
    `New<Agent><Toolset>AgentToolsetRegistration` that delegate to the provider
    helpers while keeping routing metadata centralized with the exporting agent.
  - Applications pass `runtime.AgentToolOption` values (system prompts, aggregators,
    JSON‑only behavior, telemetry) and register with `rt.RegisterToolset`.

In both cases, `Uses` merges tool specs into the consuming agent’s tool universe so
planners see a single, coherent tool catalog regardless of how tools are wired.

### Tool-based aggregation, no ResponseFormat

Agent-as-tool registrations now aggregate child tool results by invoking a real tool (`runtime.ToolResultFinalizer`) instead of relying on provider-specific `response_format` settings. The runtime captures child results, `BuildAggregationFacts` constructs a canonical payload, and the aggregation tool executes via the same `execute_tool` activity path as every other service-backed tool. The legacy ResponseFormat plumbing has been removed from the model clients (Bedrock/OpenAI); applications that need JSON-only responses should express that as a typed aggregation tool.

### Automatic thinking/event capture

By default, planners no longer need to emit streaming events. The runtime decorates the per‑turn `model.Client` returned by `AgentContext.ModelClient(id)` so:

- Streaming Recv() calls automatically publish assistant text and thinking blocks to the runtime bus and append them to the per‑turn provider ledger.
- Unary Complete() emits assistant text and usage once at the end.
- The Bedrock client validates message ordering when thinking is enabled (assistant messages with tool_use must start with thinking) and fails fast with a precise error instead of a provider 400.

This means planners only pass new messages (system/user/tool_result) and the `RunID`; rehydration of prior provider‑ready messages is handled by the runtime via a Temporal workflow query wired into the Bedrock adapter.

## Agent Toolsets vs MCP (Cross‑Service Tools)

Agent‑as‑Tool and external toolsets follow a simple rule: decode at the executor, never in the planner/transport.

- Single decode authority: only the tool executor decodes payloads.
- Byte preservation: carry raw JSON for cross‑service calls.
- Stable identity: use provider tool IDs (no string surgery).
- Decode once: no intermediate map[string]any coercions.

### When to use Toolset vs AgentToolset

- `Toolset(X)` (preferred when possible):
  - Use when you have an expression handle (e.g., a top‑level `var X = Toolset("name", ...)` or an agent’s exported toolset).
  - Goa‑AI infers the provider automatically when exactly one agent in another service exports a toolset with the same name. In that case, the consumer advertises provider tool IDs and reuses provider specs.
  - If the toolset is owned by the same service/agent, it remains local.

- `Use(AgentToolset(service, agent, toolset))` (be explicit):
  - Use when you don’t have an expression handle, or there is ambiguity (multiple agents export the same toolset name), or you want explicitness.
  - This pins the provider (service/agent/toolset) in the design and avoids name ambiguity.

### MCP Toolsets

- Define MCP toolsets with `MCPToolset(service, suite)` at the provider service and reference them via `Use(MCPToolset(...))` inside consumer agents.
- Generated registration sets `DecodeInExecutor=true` so raw JSON is passed through to the MCP executor, which decodes using its own codecs.

### Planner and Executors

- Planners should forward `json.RawMessage` for cross‑service/MCP tool calls and only decode local toolsets for ergonomics when needed.
- Executors decode using the generated `PayloadCodec`/`ResultCodec` for their toolsets.

#### Used‑Tools decode and validation (agent side)
- Agent used‑tools perform a lenient JSON decode for payloads: required‑field validation is not enforced during `FromJSON` on the agent side.
- After lenient decode, your executor (or generated per‑tool callers) runs and may map the lenient tool args into the service method payload (e.g., inject server‑owned context like `session_id` from `ToolCallMeta`).
- Strict validation happens at the service boundary (Goa service). If the service returns validation errors, you may map them to `RetryHint` in your runtime layer as desired.

Agent‑as‑Tool and external toolsets follow a simple rule: decode at the executor, never in the planner/transport.

- Define MCP suites via `MCPToolset(...)` and consume them with `Use(MCPToolset(...))`.
- For agent‑to‑agent calls, use `Use(AgentToolset(service, agent, toolset))` to consume another agent’s exported toolset.
- The DSL and codegen infer locality automatically:
  - Local toolsets execute in‑process.
  - Agent‑as‑tool executes inline (no activity) and preserves payload bytes.
  - MCP toolsets register with `DecodeInExecutor=true`, so the runtime forwards raw JSON to the executor, which decodes using its own codecs.

This keeps ownership clear (executor decodes), prevents cross‑package type mismatches, and makes transports byte‑preserving by default.

- Declare MCP servers in your Goa services as before.
- Reference suites from agents using `Use(MCPToolset(service, suite))`.
- Supply MCP callers via the generated agent config (`MCPCallers[<toolsetID>] = caller`). The agent registry automatically invokes the generated helper (`Register<Service><Suite>Toolset`) for each MCP toolset entry, wiring schemas, retry hints, and OTEL-aware transports (HTTP/SSE/stdio) into the runtime.

## Learning Resources

- **DSL reference:** `docs/dsl.md` (all DSL functions, contexts, and examples)
- **Runtime guide:** `docs/runtime.md` (engine adapters, hooks, telemetry, memory semantics)
- **Migration guide:** `docs/migration.md` (legacy goa-ai → agents framework)
- **Architecture plan:** `docs/plan.md` (data structures, templates, roadmap)
- **Example walkthrough:** `docs/examples-chat-data-loop.md` (chat data loop harness + MCP integration)
- **Integration tests:** `integration_tests/tests` (scenarios auto-run with `go test ./...`)

## Requirements

- Go 1.24+
- Goa v3.22.2+
- Temporal SDK v1.37.0 (adapter auto-wires OTEL interceptors)
- MongoDB & Redis/Pulse (default memory + stream implementations; optional via feature modules)

## Contributing

Issues and PRs are welcome! Please include a Goa design, failing test, or clear reproduction steps. See `AGENTS.md` for repository-specific guidelines (e.g., no `git` commands inside the workspace).

## License

MIT License © Goa community.
