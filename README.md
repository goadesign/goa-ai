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
> - [Migration guide (legacy goa-ai → new framework)](docs/migration.md)

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
    agentsdsl "goa.design/goa-ai/agents/dsl"
)

var _ = Service("orchestrator", func() {
    Description("Chat orchestrator")

    agentsdsl.Agent("orchestrator.chat", func() {
        Description("LLM-driven planner")

        agentsdsl.Tools(func() {
            Toolset("atlas.read", func() {
                Tool("FindEvents", func() {
                    Description("Query recent Atlas events")
                    Args(func() {
                        Attribute("query", String, "Search expression")
                        Required("query")
                    })
                    Return(func() {
                        Attribute("summary", String, "Natural language summary")
                    })
                })
            })
            agentsdsl.UseMCPToolset("assistant", "assistant-mcp")
        })

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

### 4. Wire the runtime + Temporal engine
```go
package main

import (
    "context"

    "go.temporal.io/sdk/client"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    runtimeTemporal "goa.design/goa-ai/agents/runtime/engine/temporal"
    "goa.design/goa-ai/agents/runtime/runtime"
    basicpolicy "goa.design/goa-ai/features/policy/basic"
    memorymongo "goa.design/goa-ai/features/memory/mongo"
    runmongo "goa.design/goa-ai/features/run/mongo"
    pulse "goa.design/goa-ai/features/stream/pulse"
    "goa.design/goa-ai/agents/runtime/telemetry"
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

    handle, err := rt.StartRun(context.Background(), runtime.RunInput{
        AgentID:  "orchestrator.chat",
        SessionID: "session-1",
        Messages: []planner.AgentMessage{{Role: "user", Content: "Summarize the latest status"}},
    })
    if err != nil {
        panic(err)
    }
    var out runtime.RunOutput
    if err := handle.Wait(context.Background(), &out); err != nil {
        panic(err)
    }
}
```

### 5. Explore the example
`example/` contains the **chat data loop** harness: it registers the generated MCP toolset helper, runs the runtime harness (in-process engine), and demonstrates planners invoking MCP tools with streaming + memory hooks. Run it via:
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
| **DSL (`agents/dsl`)** | Define agents, toolsets, policies, MCP suites inside Goa services. |
| **Codegen (`agents/codegen`)** | Emit tool codecs/specs, registries, Temporal workflows, activity handlers, MCP adapters. |
| **Runtime (`agents/runtime`)** | Durable plan/execute loop with policy enforcement, memory/session stores, hook bus, telemetry. |
| **Engine (`agents/runtime/engine`)** | Abstract workflow API; Temporal adapter ships with OTEL interceptors, auto-start workers, and context propagation. |
| **Features (`features/*`)** | Optional modules (Mongo memory/session, Pulse stream sink, MCP callers, Bedrock/OpenAI model clients, policy adapters). |

See `docs/plan.md` for a deep dive into generated structures, templates, and runtime packages.

## MCP + External Toolsets

- Declare MCP servers in your Goa services as before.
- Reference suites from agents using `agentsdsl.UseMCPToolset(service, suite)`.
- Supply MCP callers via the generated agent config (`MCPCallers[<toolsetID>] = caller`). The agent registry automatically invokes the generated helper (`Register<Service><Suite>Toolset`) for each `UseMCPToolset` entry, wiring schemas, retry hints, and OTEL-aware transports (HTTP/SSE/stdio) into the runtime.

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
