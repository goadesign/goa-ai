<p align="center">
  <a href="https://goa.design">
    <img alt="Goa-AI" src="https://raw.githubusercontent.com/goadesign/goa-ai/main/docs/img/goa-ai-banner.png" width="50%">
  </a>
</p>

<p align="center">
  <a href="https://github.com/goadesign/goa-ai/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/goadesign/goa-ai?style=for-the-badge"></a>
  <a href="https://goa.design/docs/8-goa-ai/"><img alt="Documentation" src="https://img.shields.io/badge/docs-goa.design-blue.svg?style=for-the-badge"></a>
  <a href="https://pkg.go.dev/goa.design/goa-ai"><img alt="Go Doc" src="https://img.shields.io/badge/godoc-reference-blue.svg?style=for-the-badge"></a>
  <a href="https://github.com/goadesign/goa-ai/actions/workflows/ci.yml"><img alt="GitHub Action: CI" src="https://img.shields.io/github/actions/workflow/status/goadesign/goa-ai/ci.yml?branch=main&style=for-the-badge"></a>
  <a href="https://goreportcard.com/report/goa.design/goa-ai"><img alt="Go Report Card" src="https://goreportcard.com/badge/goa.design/goa-ai?style=for-the-badge"></a>
  <a href="/LICENSE"><img alt="Software License" src="https://img.shields.io/badge/license-MIT-brightgreen.svg?style=for-the-badge"></a>
</p>

# Goa-AI: Design-First Agentic Systems in Go

**📚 [Full Documentation](https://goa.design/docs/8-goa-ai/)** · **🚀 [Getting Started](https://goa.design/docs/8-goa-ai/1-getting-started/)** · **💡 [Tutorials](https://goa.design/docs/8-goa-ai/3-tutorials/)**

Build intelligent, tool-wielding agents with the confidence of strong types and the power of durable execution. Goa-AI brings the design-first philosophy you love from [Goa](https://goa.design) to the world of AI agents—declare your agents, toolsets, and policies in a clean DSL, and let code generation handle the rest.

No more hand-rolled JSON schemas. No more brittle tool wiring. No more wondering if your agent will survive a restart. Just elegant designs that compile into production-grade systems.

## Why Goa-AI?

| Challenge | How Goa-AI Helps |
|---|---|
| **Zero-Glue Tooling** | Bind agents directly to existing Goa services with `BindTo`. No adapters, no glue code, just instant tools. |
| **LLM workflows feel fragile** | Type-safe tool payloads with validations and examples—no ad-hoc JSON guessing games |
| **Long-running agents crash** | Durable orchestration via Temporal with automatic retries, time budgets, and deterministic replay |
| **Composing agents is messy** | First-class agent-as-tool composition, even across processes, with unified history |
| **Schema drift haunts you** | Generated codecs and registries keep everything in sync—change the DSL, regenerate, done |
| **Observability is an afterthought** | Built-in streaming, transcripts, logs, metrics, and traces from day one |
| **MCP integration is manual** | Generated wrappers turn MCP servers into typed toolsets automatically |

## The Mental Model

```
Define Intent → Generate Infrastructure → Execute Reliably
```

Think of it as a pipeline from intention to execution:

1. **Define Intent** (`dsl`) — Express what you want: agents, tools, policies. Clean, declarative, version-controlled.

2. **Generate Infrastructure** (`codegen`) — Transform your design into typed Go packages: tool specs, codecs, workflow definitions, registry helpers. Lives under `gen/`—never edit by hand.

3. **Execute Reliably** (`runtime`) — The workhorse that executes your agents: plan/execute loops, policy enforcement, memory, sessions, streaming, telemetry, and MCP integration.

4. **Engine** — Swap backends without changing code. In-memory for fast iteration; Temporal for production durability.

5. **Features** — Plug in what you need: Mongo for memory/sessions/runs, Pulse for real-time streams, Bedrock/OpenAI/Gateway model clients, policy engines.

## Quick Start

### 1. Install Goa & Goa-AI
```bash
go install goa.design/goa/v3/cmd/goa@latest
go get goa.design/goa-ai
```

### 2. Define Your Agent

Create `design/design.go`:

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
)

var _ = API("orchestrator", func() {})

var Ask = Type("Ask", func() {
    Attribute("question", String, "User question")
    Example(map[string]any{"question": "What is the capital of Japan?"})
    Required("question")
})

var Answer = Type("Answer", func() {
    Attribute("text", String, "Answer text")
    Required("text")
})

var _ = Service("orchestrator", func() {
    Agent("chat", "Friendly Q&A agent", func() {
        Use("helpers", func() {
            Tool("answer", "Answer a simple question", func() {
                Args(Ask)
                Return(Answer)
            })
        })
        RunPolicy(func() {
            DefaultCaps(MaxToolCalls(8), MaxConsecutiveFailedToolCalls(2))
            TimeBudget("2m")
        })
    })
})
```

### 3. Generate Code
```bash
goa gen example.com/assistant/design
```

This produces agent packages under `gen/orchestrator/agents/...`, tool codecs/specs, planner configs, and Temporal activities.

> **Note:** After generation, a contextual guide named `AGENTS_QUICKSTART.md` is written at the module root to summarize what was generated and how to use it. To opt out, call `DisableAgentDocs()` inside your API DSL.

### 4. Wire the Runtime

```go
package main

import (
    "context"
    "fmt"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
)

// StubPlanner is a minimal planner (implementation omitted, see AGENTS_QUICKSTART.md)
type StubPlanner struct{}

func (p *StubPlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
                Role:  model.ConversationRoleAssistant,
                Parts: []model.Part{model.TextPart{Text: "Hello!"}},
            },
        },
    }, nil
}

func (p *StubPlanner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
    return nil, nil
}

func main() {
    rt := runtime.New() // in-memory engine by default

    // 1. Register the agent
    chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{
        Planner: &StubPlanner{},
    })

    // 2. Run it!
    client := chat.NewClient(rt)
    out, _ := client.Run(context.Background(), []*model.Message{{
        Role:  model.ConversationRoleUser,
        Parts: []model.Part{model.TextPart{Text: "Say hi"}},
    }})

    fmt.Println("RunID:", out.RunID)
}
```

## Debuggable by Design

Agents are notoriously hard to debug. Goa-AI makes it transparent.

- **Transcript Ledger**: Every thinking block, tool call, and result is recorded in exact provider order.
- **Distributed Tracing**: Trace a decision from **Thinking → Tool Call → Result → Final Answer** without grepping logs.
- **Structured Logs**: No more parsing raw text. Get structured events for every state change.

```go
// Enable the Clue telemetry stack
rt := runtime.New(runtime.Options{
    Logger:  telemetry.NewClueLogger(),
    Metrics: telemetry.NewClueMetrics(),
    Tracer:  telemetry.NewClueTracer(),
})
```

## Architecture Overview

| Layer | Responsibility |
| --- | --- |
| **DSL (`dsl`)** | Define agents, toolsets, policies, MCP suites inside Goa services. |
| **Codegen (`codegen/agent`, `codegen/mcp`)** | Emit tool codecs/specs, registries, Temporal workflows, activity handlers, MCP helpers. |
| **Runtime (`runtime/agent`, `runtime/mcp`)** | Durable plan/execute loop with policy enforcement, memory/session stores, hook bus, telemetry, MCP callers. |
| **Engine (`runtime/agent/engine`)** | Abstract workflow API; Temporal adapter ships with OTEL interceptors, auto-start workers, and context propagation. |
| **Features (`features/*`)** | Optional modules (Mongo memory/session, Pulse stream sink, MCP callers, Bedrock/OpenAI model clients, policy engine). |

## Toolsets: Where the Magic Happens

Toolsets are the heart of Goa-AI. They define what your agents can do, with full type safety and validation.

### Defining Tools

The simplest approach: define tool schemas inline with `Args` and `Return`. You provide the executor implementation.

```go
var _ = Service("orchestrator", func() {
    Agent("assistant", "Helpful assistant", func() {
        Use("utils", func() {
            Tool("summarize", "Summarize text content", func() {
                Args(func() {
                    Attribute("text", String, "Text to summarize")
                    Required("text")
                })
                Return(func() {
                    Attribute("summary", String, "Condensed summary")
                })
            })
        })
    })
})
```

### Binding Tools to Service Methods with `BindTo`

Have existing Goa service methods? Bind tools directly to them—your service logic becomes instantly available to LLMs with zero glue code.

```go
var _ = Service("documents", func() {
    // Your existing Goa service method
    Method("search", func() {
        Payload(func() {
            Attribute("query", String, "Search query")
            Attribute("session_id", String, "Session identifier")
            Required("query", "session_id")
        })
        Result(func() {
            Attribute("results", ArrayOf(Document))
            Attribute("total", Int)
        })
    })

    Agent("assistant", "Document search assistant", func() {
        Use("doc-tools", func() {
            Tool("search_docs", "Search documents by query", func() {
                // Tool schema can differ from method payload
                Args(func() {
                    Attribute("query", String, "What to search for")
                    Required("query")
                })
                Return(func() {
                    Attribute("results", ArrayOf(Document))
                })
                // Bind to the service method - codegen handles the mapping
                BindTo("search")
                // Hide infrastructure fields from the LLM
                Inject("session_id")
            })
        })
    })
})
```

**What `BindTo` gives you:**
- **Schema flexibility**: Tool Args/Return can differ from method Payload/Result
- **Auto-generated transforms**: Codegen creates type-safe mappers between tool and method types
- **Field injection**: Use `Inject` to hide infrastructure fields (session IDs, auth tokens) from the LLM
- **Validation at boundaries**: Method payload validation still applies, errors become retry hints

### Tool Implementation Patterns

Goa-AI supports four ways to implement tools, each optimized for different scenarios. The framework generates the boilerplate; you focus on business logic.

| Pattern | DSL | What Codegen Generates | You Implement |
|---------|-----|------------------------|---------------|
| **Inline tools** | `Tool` with `Args`/`Return` | Specs, codecs, JSON schemas | Custom executor via `RegisterUsedToolsets` |
| **Method-bound** | `Tool` + `BindTo` | Specs, codecs, transforms, executor factory | Wire service client to generated executor |
| **Agent-as-tool** | `Export` + `Use` | Provider helpers, consumer registration, inline execution | Planner for the nested agent |
| **MCP tools** | `MCPToolset` + `Use` | MCP executor, caller wiring | Provide `mcpruntime.Caller` in config |

**Inline tools** give you maximum flexibility—define any schema and implement the executor however you like. Great for tools that don't map to existing services or need custom logic.

**Method-bound tools** are the sweet spot for most projects. You already have Goa services with validated payloads and results; `BindTo` lets agents call them directly. Codegen handles the type mapping, and `Inject` keeps infrastructure fields (session IDs, auth tokens) hidden from the LLM.

**Agent-as-tool** enables hierarchical agent architectures. A specialist agent (data analyst, code reviewer, researcher) can be invoked as a tool by an orchestrator. The nested agent runs inline in the same workflow—single transaction, unified history, no network hops
required.

**MCP tools** integrate external Model Context Protocol servers. Define the schema once, and Goa-AI generates typed wrappers with retry logic, tracing, and transport handling (HTTP, SSE, stdio).

All patterns produce the same artifacts:
- **Type-safe payload/result structs** under `gen/<svc>/agents/<agent>/specs/<toolset>/`
- **JSON codecs** with `PayloadCodec(toolID)` and `ResultCodec(toolID)`
- **Tool schemas JSON** at `gen/<svc>/agents/<agent>/specs/tool_schemas.json`

### Agent-as-Tool (Composable Agents)

Define tools in an `Export` block, and other agents can `Use` them seamlessly. Nested agents execute inline within the parent workflow history—single transaction, unified debugging, elegant composition.

```go
// Provider agent exports a toolset
Agent("data-analyst", "Expert at data queries", func() {
    Export("analysis", func() {
        Tool("analyze", "Deep analysis of datasets", func() {
            Args(AnalysisRequest)
            Return(AnalysisResult)
        })
    })
})

// Consumer agent uses it as a tool
Agent("orchestrator", "Main chat agent", func() {
    Use(AgentToolset("service", "data-analyst", "analysis"))
})
```

### Universal MCP Adapter

Goa-AI is a two-way MCP bridge. **Write once, run anywhere.** Your logic runs as a service method, an agent tool, or an MCP server—simultaneously.

**1. Consuming MCP:** Import any MCP server as a typed toolset.

```go
// Use an external MCP server as a toolset
var AssistantSuite = MCPToolset("assistant", "assistant-mcp")

Agent("chat", "Chat agent with MCP tools", func() {
    Use(AssistantSuite)
})
```

**2. Serving MCP:** Expose your Goa service as an MCP server automatically.

```go
Service("calculator", func() {
    MCPServer("calc", "1.0.0") // Expose service as MCP server
    Method("add", func() {
        Payload(func() { Attribute("a", Int); Attribute("b", Int) })
        Result(Int)
        MCPTool("add", "Add two numbers") // Export this method as an MCP tool
    })
})
```

### Tool Schemas JSON

Every agent gets a backend-agnostic JSON catalogue at `gen/<service>/agents/<agent>/specs/tool_schemas.json`:

```json
{
  "tools": [
    {
      "id": "toolset.tool",
      "service": "orchestrator",
      "toolset": "helpers",
      "title": "Answer a simple question",
      "description": "Answer a simple question",
      "payload": { "name": "Ask", "schema": { /* JSON Schema */ } },
      "result": { "name": "Answer", "schema": { /* JSON Schema */ } }
    }
  ]
}
```

## The Plan → Execute → Resume Loop

1. **Start** — The runtime spins up a workflow for your agent (in-memory or Temporal)
2. **Plan** — Your planner's `PlanStart` receives the conversation and decides: final answer or tool calls?
3. **Execute** — Tool calls run through generated codecs, validated and type-safe
4. **Resume** — `PlanResume` gets tool results; the loop continues until a final response or policy limits hit
5. **Stream** — Events flow to UIs; transcripts persist if configured

### Policies Keep Things Sane

Per-turn enforcement of:
- Maximum tool calls
- Consecutive failure limits
- Time budgets
- Tool allowlists via policy engines

### Three Flavors of Tool Execution

| Type                | How It Works                                                                         |
|---------------------|--------------------------------------------------------------------------------------|
| **Native toolsets** | Your implementations + generated codecs = typed, validated tools                     |
| **Agent-as-tool**   | Nested agent runs inline within the same workflow history                            |
| **MCP toolsets**    | Generated wrappers handle JSON schemas, transport (HTTP/SSE/stdio), retries, tracing |

## Human-in-the-Loop

Agents can pause mid-run to request human input or external tool results:

```go
// Pause a run for human review
rt.PauseRun(ctx, interrupt.PauseRequest{
    RunID:       "session-1-run-1",
    Reason:      "human_review",
    RequestedBy: "policy-engine",
})

// Resume after approval
rt.ResumeRun(ctx, interrupt.ResumeRequest{
    RunID:  "session-1-run-1",
    Notes:  "Reviewer approved",
})
```

The workflow loop drains pause/resume signals via the interrupt controller, updates the run store, and emits `run_paused` / `run_resumed` hook events so Pulse subscribers stay in sync.

## Streaming for UIs

Push real-time events to WebSocket/SSE or a message bus for live agent experiences.

```go
// Global broadcast (all runs)
sink := &MySink{}
rt := runtime.New(runtime.WithStream(sink))

// Per-run streaming (per UI tab)
closeFn, err := rt.SubscribeRun(ctx, runID, sink)
defer closeFn()
```

The hook bus publishes structured events (`tool_start`, `tool_result`, `assistant_message`, `planner_thought`, ...) that memory stores persist and stream sinks carry to real-time UIs.

## Provider-Precise Transcript Ledger

Long-running agents need to rebuild provider payloads exactly—thinking blocks, tool calls, and results in the precise order providers expect. The **transcript ledger** solves this:

- **Provider fidelity**: Preserves the exact ordering required by providers (thinking → tool_use → tool_result)
- **Deterministic replay**: Stateless API safe for Temporal workflow replay
- **Provider-agnostic storage**: JSON-friendly format converts to/from provider formats at edges
- **Minimal footprint**: Stores only what's needed to rebuild payloads, nothing more

The runtime automatically maintains the ledger, so planners get correct message history without manual bookkeeping.

## Automatic Thinking/Event Capture

By default, planners no longer need to emit streaming events. The runtime decorates the per-turn `model.Client` returned by `AgentContext.ModelClient(id)` so:

- Streaming `Recv()` calls automatically publish assistant text and thinking blocks to the runtime bus
- Unary `Complete()` emits assistant text and usage once at the end
- The Bedrock client validates message ordering when thinking is enabled and fails fast with a precise error instead of a provider 400

This means planners only pass new messages (system/user/tool_result) and the `RunID`; rehydration of prior provider-ready messages is handled by the runtime.

## Feature Modules

| Package                  | Purpose                                                |
|--------------------------|--------------------------------------------------------|
| `features/memory/mongo`  | Mongo-backed memory store for transcripts              |
| `features/session/mongo` | Mongo-backed session store for multi-turn state        |
| `features/run/mongo`     | Mongo-backed run store for run metadata                |
| `features/stream/pulse`  | Pulse message bus sink for real-time streaming         |
| `features/model/bedrock` | AWS Bedrock model client (Claude, etc.)                |
| `features/model/openai`  | OpenAI-compatible model client                         |
| `features/model/gateway` | Remote model gateway for centralized model serving     |
| `features/policy/basic`  | Basic policy engine for tool filtering and caps        |

## Start Simple, Scale Infinite

Goa-AI runs anywhere. Start with the in-memory engine on your laptop. When you're ready for production, swap in Temporal, Mongo, and Pulse without changing your agent logic.

**Production Wiring Example:**

```go
func main() {
    // 1. Durable Engine (Temporal)
    temporalEng, _ := runtimeTemporal.New(runtimeTemporal.Options{
        ClientOptions: &client.Options{HostPort: "127.0.0.1:7233", Namespace: "default"},
        WorkerOptions: runtimeTemporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
    })
    defer temporalEng.Close()

    // 2. Persistence & Streaming
    mongoClient := newMongoClient()
    redisClient := newRedisClient()
    pulseSink, _ := pulse.NewSink(pulse.Options{Client: redisClient})

    // 3. Production Runtime
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

    chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
        Planner:        newChatPlanner(),
        PromptProvider: chat.NewPromptProvider(),
    })

    // Workers poll and execute; clients submit runs from anywhere
}
```

## Best Practices

**Design first** — Put all agent and tool schemas in the DSL. Add examples and validations. Let codegen own schemas and codecs.

**Never hand-encode** — Use generated codecs and clients everywhere. Avoid `json.Marshal`/`Unmarshal` for tool payloads.

**Keep planners focused** — Planners decide *what* (final answer vs. which tools). Tool implementations handle *how*.

**Split client from worker** — Register agents on workers; use generated typed clients from other processes to submit runs.

**Compose with export/use** — Prefer agent-as-tool over brittle cross-service contracts. Single history, unified debugging.

**Regenerate often** — DSL change → `goa gen` → lint/test → run. Never edit `gen/` manually.

## Learning Resources

| Topic           | Resource                                               |
|-----------------|--------------------------------------------------------|
| DSL reference   | [`docs/dsl.md`](docs/dsl.md)                           |
| Runtime guide   | [`docs/runtime.md`](docs/runtime.md)                   |
| Overview        | [`docs/overview.md`](docs/overview.md)                 |
| MCP integration | `codegen/mcp` and `runtime/mcp`                        |
| Features        | `features/*` (memory, session, run, stream, model clients) |
| Integration tests | `integration_tests/tests` (scenarios auto-run with `go test ./...`) |

## Requirements

- Go 1.24+
- Goa v3.22.2+
- Temporal SDK v1.37.0 (adapter auto-wires OTEL interceptors)
- MongoDB & Redis/Pulse (default memory + stream implementations; optional via feature modules)

## Contributing

Issues and PRs are welcome! Please include a Goa design, failing test, or clear reproduction steps. See `AGENTS.md` for repository-specific guidelines.

## License

MIT License © Raphael Simon & [Goa community](https://goa.design).

---

*Build agents that are a joy to develop and a breeze to operate. Welcome to Goa-AI.*
