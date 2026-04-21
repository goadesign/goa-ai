<p align="center">
  <a href="https://goa.design">
    <img alt="Goa-AI" src="https://raw.githubusercontent.com/goadesign/goa-ai/main/docs/img/goa-ai-banner.png" width="50%">
  </a>
</p>

<p align="center">
  <a href="https://github.com/goadesign/goa-ai/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/goadesign/goa-ai?style=for-the-badge"></a>
  <a href="https://goa.design/docs/2-goa-ai/"><img alt="Documentation" src="https://img.shields.io/badge/docs-goa.design-blue.svg?style=for-the-badge"></a>
  <a href="https://pkg.go.dev/goa.design/goa-ai"><img alt="Go Doc" src="https://img.shields.io/badge/godoc-reference-blue.svg?style=for-the-badge"></a>
  <a href="https://github.com/goadesign/goa-ai/actions/workflows/ci.yml"><img alt="GitHub Action: CI" src="https://img.shields.io/github/actions/workflow/status/goadesign/goa-ai/ci.yml?branch=main&style=for-the-badge"></a>
  <a href="https://goreportcard.com/report/goa.design/goa-ai"><img alt="Go Report Card" src="https://goreportcard.com/badge/goa.design/goa-ai?style=for-the-badge"></a>
  <a href="LICENSE"><img alt="Software License" src="https://img.shields.io/badge/license-MIT-brightgreen.svg?style=for-the-badge"></a>
</p>

<h1 align="center">Design-First AI Agents in Go</h1>

<p align="center">
  <b>Define your agents in code. Generate the infrastructure. Execute with confidence.</b>
</p>

<p align="center">
  <a href="https://goa.design/docs/2-goa-ai/">📚 Documentation</a> ·
  <a href="https://goa.design/docs/2-goa-ai/quickstart/">🚀 Quickstart</a> ·
  <a href="https://goa.design/docs/2-goa-ai/testing/">🧪 Testing</a> ·
  <a href="#quick-start">⚡ Try It Now</a>
</p>

---

## Stop Writing Fragile Agent Code

Building AI agents shouldn't mean wrestling with JSON schemas, debugging brittle tool wiring, or losing progress when processes crash. Yet that's what most frameworks offer—imperative code scattered across files, no contracts, and "good luck" when things break.

**Goa-AI takes a different approach.** Define your agents, tools, and policies in a typed DSL. Let code generation handle the infrastructure. Run on a durable engine that survives failures. What you get:

| Pain Point | Goa-AI Solution |
|------------|-----------------|
| **Hand-rolled JSON schemas** | Type-safe tool definitions with validations—schemas generated automatically |
| **Brittle tool wiring** | `BindTo` connects tools directly to Goa service methods. Zero glue code |
| **Agents that crash and lose state** | Temporal-backed durable execution with automatic retries |
| **Messy multi-agent composition** | First-class agent-as-tool with run trees and run links |
| **Schema drift between components** | Single source of truth: DSL → generated codecs → runtime validation |
| **Hand-parsed structured final answers** | Service-owned `Completion(...)` contracts generate schemas, codecs, and typed completion helpers |
| **Observability as afterthought** | Built-in streaming, transcripts, traces, and metrics from day one |
| **Manual MCP integration** | Generated wrappers turn MCP servers into typed toolsets |
| **Toolsets scattered across services** | Generated registry clients plus a clustered registry service for dynamic discovery and health-monitored invocation |

---

## How It Works

```
     ┌─────────────────┐        ┌─────────────────┐        ┌─────────────────┐
     │    1. DESIGN    │   →    │   2. GENERATE   │   →    │   3. EXECUTE    │
     │                 │        │                 │        │                 │
     │  Agent DSL      │        │  Tool specs     │        │  Plan/Execute   │
     │  Tool schemas   │        │  Codecs         │        │  Policy checks  │
     │  Completions    │        │  Completions    │        │  Structured I/O │
     │  Policies       │        │  Workflows      │        │  Streaming      │
     │  MCP bindings   │        │  Registries     │        │  Memory         │
     └─────────────────┘        └─────────────────┘        └─────────────────┘
           design/                    gen/                    runtime/
```

1. **Design** — Express intent in Go: agents, tools, policies. Version-controlled, type-checked, reviewable.

2. **Generate** — `goa gen` produces everything: tool specs with JSON schemas, service-owned completion packages, type-safe codecs, workflow definitions, and generated registry clients/helpers. Never edit `gen/`—regenerate on change.

3. **Execute** — The runtime runs your agents: plan/execute loops, policy enforcement, memory persistence, event streaming. Swap engines (in-memory → Temporal) without changing agent code.

---

## Registry Vocabulary

`goa-ai` uses "registry" for a few adjacent concepts:

- `Registry(...)` / `FromRegistry(...)` in the DSL declare an external tool catalog and a dynamic toolset reference that is resolved at runtime.
- `gen/<service>/registry/<name>/` contains generated agent-side registry clients and helpers for one declared DSL registry source.
- `runtime/toolregistry` defines the low-level wire protocol and Pulse stream naming shared by registry executors, providers, and the clustered gateway.
- `registry/` contains the standalone clustered registry service implementation that admits toolsets, tracks provider health, and routes cross-process tool calls.

Keeping these layers distinct helps when reading generated `registry.go` files: those helpers register components with a local `agentsruntime.Runtime`; they do not implement the clustered `registry/` service.

---

## Quick Start

### Install

```bash
go install goa.design/goa/v3/cmd/goa@latest
```

### Define an Agent

Create `design/design.go`:

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
)

var _ = Service("demo", func() {
    Agent("assistant", "A helpful assistant", func() {
        Use("weather", func() {
            Tool("get_weather", "Get current weather for a city", func() {
                Args(func() {
                    Attribute("city", String, "City name", func() {
                        MinLength(2)
                        Example("Tokyo")
                    })
                    Required("city")
                })
                Return(func() {
                    Attribute("temperature", Int, "Temperature in Celsius")
                    Attribute("conditions", String, "Weather description")
                    Required("temperature", "conditions")
                })
            })
        })
    })
})
```

### Generate

```bash
mkdir myagent && cd myagent
go mod init myagent
go get goa.design/goa/v3@latest goa.design/goa-ai@latest

# Create design/design.go with the code above

goa gen myagent/design
```

Generated artifacts in `gen/`:
- **Tool specs** with JSON schemas for LLM function calling
- **Type-safe codecs** for payload/result serialization
- **Agent registration helpers** and typed clients
- **Workflow definitions** for durable execution

### Run

```go
package main

import (
    "context"
    "fmt"

    assistant "myagent/gen/demo/agents/assistant"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
)

func main() {
    ctx := context.Background()
    rt := runtime.New() // in-memory engine, zero dependencies

    // Register the agent with a planner (decision-maker) and executor (tool runner)
    assistant.RegisterAssistantAgent(ctx, rt, assistant.AssistantAgentConfig{
        Planner:  &MyPlanner{},   // Calls LLM to decide: tools or final answer?
        Executor: &MyExecutor{},  // Runs tool logic when planner requests it
    })

    // Run with a user message
    client := assistant.NewClient(rt)
    out, _ := client.Run(ctx, []*model.Message{{
        Role:  model.ConversationRoleUser,
        Parts: []model.Part{model.TextPart{Text: "What's the weather in Paris?"}},
    }})

    fmt.Println("RunID:", out.RunID)
    // → Agent calls get_weather tool, receives result, synthesizes response
}
```

> **Planner + Executor pattern**: Planners decide *what* (final answer or which tools). Executors decide *how* (tool implementation). The runtime handles the loop, policy enforcement, and state.

---

## Core Concepts

### Toolsets: What Agents Can Do

Toolsets are collections of capabilities your agents can invoke. Define them with full type safety:

```go
Agent("assistant", "Document assistant", func() {
    Use("docs", func() {
        Tool("search", "Search documents", func() {
            Args(func() {
                Attribute("query", String, "Search query", func() {
                    MinLength(1)
                    MaxLength(500)
                })
                Attribute("limit", Int, "Max results", func() {
                    Minimum(1)
                    Maximum(100)
                    Default(10)
                })
                Required("query")
            })
            Return(ArrayOf(Document))
        })
    })
})
```

**What you get:**
- JSON Schema for LLM function calling (auto-generated)
- Validation at boundaries—invalid calls get retry hints, not crashes
- Type-safe Go structs for payloads and results

### Typed Direct Completions

Not every structured model interaction is a tool call. Sometimes you want the
assistant to return a typed result directly.

```go
var TaskDraft = Type("TaskDraft", func() {
    Attribute("name", String, "Task name")
    Attribute("goal", String, "Outcome-style goal")
    Required("name", "goal")
})

var _ = Service("tasks", func() {
    Completion("draft_from_transcript", "Produce a task draft directly", func() {
        Return(TaskDraft)
    })
})
```

Completion names are part of the structured-output contract. They must be
1-64 ASCII characters, may contain letters, digits, `_`, and `-`, and must
start with a letter or digit.

`goa gen` emits a service-owned package at `gen/<service>/completions/` with:

- JSON schema for the completion result
- typed codecs and validation helpers
- typed `completion.Spec` values
- unary `Complete<Name>(...)` helpers
- streaming `StreamComplete<Name>(...)` and `Decode<Name>Chunk(...)` helpers

Completions are service-owned contracts and do not require any `Agent(...)`
declaration. Agent quickstart/example scaffolding is emitted only for services
that actually own agents.

This keeps direct assistant output on the same contract surface as tools:
Goa types, validations, `OneOf`, generated codecs, and fail-fast runtime
enforcement. Generated helpers reject tool-enabled requests and caller-supplied
`StructuredOutput`. Unary helpers decode the final unary response directly.
Streaming helpers stay on the raw `model.Streamer` surface: `completion_delta`
chunks are preview-only, exactly one final `completion` chunk is canonical, and
`Decode<Name>Chunk(...)` decodes only that final payload. Completion streams
stay off planner streaming helpers because planner streaming is for assistant
transcript/tool events, not structured-output chunks. Providers that do not
implement structured output surface `model.ErrStructuredOutputUnsupported`.
Provider adapters translate the canonical generated schema into any
provider-specific subset they require and fail explicitly when the provider
cannot represent the service contract.

### BindTo: Zero-Glue Service Integration

Already have Goa services? Bind tools directly to methods:

```go
// Your existing Goa service method
Method("search_documents", func() {
    Payload(func() {
        Attribute("query", String)
        Attribute("session_id", String) // Infrastructure field
        Required("query", "session_id")
    })
    Result(ArrayOf(Document))
})

Agent("assistant", "Document assistant", func() {
    Use("docs", func() {
        Tool("search", "Search documents", func() {
            Args(func() {
                Attribute("query", String, "What to search for")
                Required("query")
            })
            Return(ArrayOf(Document))
            BindTo("search_documents")  // Auto-generated transform
            Inject("session_id")         // Hidden from LLM, filled at runtime
        })
    })
})
```

**BindTo gives you:**
- Schema flexibility—tool args can differ from method payload
- Auto-generated type-safe transforms between tool and service types
- Field injection for infrastructure concerns (auth, session IDs)
- Method validation still applies; errors become retry hints

### Agent Composition: Agents as Tools

Agents can invoke other agents. Define specialist agents, compose them into orchestrators:

```go
// Specialist agent exports tools
Agent("researcher", "Research specialist", func() {
    Export("research", func() {
        Tool("deep_search", "Comprehensive research", func() {
            Args(ResearchQuery)
            Return(ResearchResult)
        })
    })
})

// Orchestrator uses the specialist
Agent("coordinator", "Main coordinator", func() {
    Use(AgentToolset("svc", "researcher", "research"))
    // coordinator can now call "research.deep_search" as a tool
})
```

**Agent-as-tool runs as a child workflow**: The nested agent executes in its own run. The parent receives a `ToolResult` with a `RunLink` handle to the child run, and streaming emits a `ChildRunLinked` link event so UIs can render nested runs without flattening.

### Policies: Guardrails for Production

Set limits on what agents can do:

```go
Agent("assistant", "Production assistant", func() {
    RunPolicy(func() {
        DefaultCaps(
            MaxToolCalls(20),                    // Total tools per run
            MaxConsecutiveFailedToolCalls(3),   // Failures before abort
        )
        TimeBudget("5m")        // Wall-clock limit
        InterruptsAllowed(true) // Enable pause/resume
    })
})
```

Policies are enforced by the runtime—not just suggestions.

---

## Streaming: Real-Time Visibility

Agents are notoriously opaque. Goa-AI streams typed events throughout execution:

```go
// Receive events as they happen
type MySink struct{}

func (s *MySink) Send(ctx context.Context, event stream.Event) error {
    switch e := event.(type) {
    case *stream.ToolStart:
        fmt.Printf("🔧 Starting: %s\n", e.Data.ToolName)
    case *stream.ToolEnd:
        fmt.Printf("✅ Completed: %s\n", e.Data.ToolName)
    case *stream.AssistantReply:
        fmt.Print(e.Data.Text) // Stream text as it arrives
    case *stream.PlannerThought:
        fmt.Printf("💭 %s\n", e.Data.Content)
    }
    return nil
}

func (s *MySink) Close(ctx context.Context) error { return nil }

// Wire to runtime
rt := runtime.New(runtime.WithStream(&MySink{}))
```

**Stream profiles** filter events for different audiences:
- `UserChatProfile()` — End-user chat UIs with nested agent cards
- `AgentDebugProfile()` — Verbose debug view with linked child runs
- `MetricsProfile()` — Usage and workflow events only for telemetry

---

## Durable Execution

The in-memory engine is great for development. For production, swap in Temporal:

```go
import (
    "context"
    "log"
    "time"

    "go.temporal.io/sdk/client"
    runtimeTemporal "goa.design/goa-ai/runtime/agent/engine/temporal"
)

func main() {
    // Production engine: Temporal for durability
    eng, err := runtimeTemporal.NewWorker(runtimeTemporal.Options{
        ClientOptions: &client.Options{
            HostPort:  "localhost:7233",
            Namespace: "default",
        },
        WorkerOptions: runtimeTemporal.WorkerOptions{
            TaskQueue: "my-agents",
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer eng.Close()

    rt := runtime.New(runtime.WithEngine(eng))
    // ... register agents, same as before
    sealCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()
    if err := rt.Seal(sealCtx); err != nil {
        log.Fatal(err)
    }
    // Start serving traffic only after Seal succeeds.
}
```

**What Temporal gives you:**
- **Crash recovery** — Workers restart; runs resume from last checkpoint
- **Automatic retries** — Failed tools retry without re-calling the LLM
- **Rate limit handling** — Exponential backoff absorbs API throttling
- **Deployment safety** — Rolling deploys don't lose in-flight work

Your agent code doesn't change. Just swap the engine.

For worker processes, `rt.Seal(ctx)` is the activation boundary: it retries
worker startup until the provided deadline and returns an error instead of
letting the process serve traffic with no active pollers.

---

## MCP Integration

Goa-AI is a two-way MCP bridge.

**Consume MCP servers as typed toolsets:**

```go
var FilesystemTools = Toolset(FromMCP("filesystem", "filesystem-mcp"))

Agent("assistant", "File assistant", func() {
    Use(FilesystemTools)
})
```

**Expose your services as MCP servers:**

```go
Service("calculator", func() {
    MCP("calc", "1.0.0") // Enable MCP protocol

    Method("add", func() {
        Payload(func() { Attribute("a", Int); Attribute("b", Int) })
        Result(Int)
        Tool("add", "Add two numbers") // Export as MCP tool
    })
})
```

Generated wrappers handle transport (HTTP, SSE, stdio), retries, and tracing.

---

## Internal Tool Registry: Cross-Process Discovery

When toolsets live in separate services that scale independently, you need dynamic discovery. The **Internal Tool Registry** is a clustered gateway that enables toolset discovery and invocation across process boundaries.

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Agent 1   │     │   Agent 2   │     │   Agent N   │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                   │                   │
       └───────────────────┼───────────────────┘
                           │ gRPC
                    ┌──────▼──────┐
                    │  Registry   │◄──── Cluster (same Name + Redis)
                    │   Nodes     │
                    └──────┬──────┘
                           │ Pulse Streams
       ┌───────────────────┼───────────────────┐
       │                   │                   │
┌──────▼──────┐     ┌──────▼──────┐     ┌──────▼──────┐
│  Provider 1 │     │  Provider 2 │     │  Provider N │
│  (Toolset)  │     │  (Toolset)  │     │  (Toolset)  │
└─────────────┘     └─────────────┘     └─────────────┘
```

**Run a registry node:**

```go
reg, _ := registry.New(ctx, registry.Config{
    Redis: redisClient,
    Name:  "my-registry",  // Nodes with same name form a cluster
})

// Blocks until shutdown
reg.Run(ctx, ":9090")
```

Or use the binary:

```bash
# Single node
REDIS_URL=localhost:6379 go run ./registry/cmd/registry

# Multi-node cluster (run on different hosts)
REGISTRY_NAME=prod REGISTRY_ADDR=:9090 REDIS_URL=redis:6379 ./registry
REGISTRY_NAME=prod REGISTRY_ADDR=:9091 REDIS_URL=redis:6379 ./registry
```

**What clustering gives you:**
- **Shared registrations** — Toolsets registered on any node are visible everywhere
- **Coordinated health checks** — Distributed tickers ensure exactly one node pings at a time
- **Automatic failover** — Connect to any node; they all serve identical state
- **Horizontal scaling** — Add nodes to handle more gRPC connections

---

## Production Configuration

A fully-wired production setup:

```go
func main() {
    // Durable execution
    eng, err := runtimeTemporal.NewWorker(runtimeTemporal.Options{
        ClientOptions: &client.Options{HostPort: "temporal:7233"},
        WorkerOptions: runtimeTemporal.WorkerOptions{TaskQueue: "agents"},
    })
    if err != nil {
        log.Fatal(err)
    }
    defer eng.Close()

    // Persistence
    mongoClient := newMongoClient() // *mongo.Client from go.mongodb.org/mongo-driver/v2/mongo
    memClient, _ := memorymongoclient.New(memorymongoclient.Options{
        Client:   mongoClient,
        Database: "agents",
    })
    memStore, _ := memorymongo.NewStore(memClient)
    redisClient := newRedisClient()

    // Model provider
    modelClient, _ := bedrock.New(bedrock.Options{
        Region: "us-east-1",
        Model:  "anthropic.claude-sonnet-4-20250514-v1:0",
    })

    // Streaming
    pulseSink, _ := pulse.NewSink(pulse.Options{Client: redisClient})

    // Wire it all together
    rt := runtime.New(
        runtime.WithEngine(eng),
        runtime.WithMemoryStore(memStore),
        runtime.WithStream(pulseSink),
        runtime.WithModelClient("claude", modelClient),
        runtime.WithPolicy(basicpolicy.New()),
        runtime.WithLogger(telemetry.NewClueLogger()),
        runtime.WithMetrics(telemetry.NewClueMetrics()),
        runtime.WithTracer(telemetry.NewClueTracer()),
    )

    // Register agents
    chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
        Planner: newChatPlanner(),
    })

    sealCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
    defer cancel()
    if err := rt.Seal(sealCtx); err != nil {
        log.Fatal(err)
    }

    // Workers now poll and execute; clients submit runs from anywhere.
}
```

## Tracing Error Contract

Runtime tracing uses one generic rule for span failures across model clients and
Temporal activity execution:

- Non-nil errors mark spans failed by default.
- They do not mark spans failed when the active context is already done and the
  returned error is a structured context-termination shape.
- Supported termination shapes are `context.Canceled`,
  `context.DeadlineExceeded`, and gRPC `Canceled` / `DeadlineExceeded`
  statuses.

This contract is runtime-generic. Application-specific error taxonomies,
dashboard semantics, and product observability attributes belong in the
integrating application, not in `goa-ai`.

---

## Feature Modules

| Package | Purpose |
|---------|---------|
| **Model Providers** | |
| `features/model/bedrock` | AWS Bedrock (Claude, Titan, etc.) |
| `features/model/openai` | OpenAI-compatible APIs |
| `features/model/anthropic` | Direct Anthropic Claude API |
| `features/model/gateway` | Remote model gateway for centralized serving |
| `features/model/middleware` | Rate limiting, logging, metrics middleware |
| **Persistence** | |
| `features/memory/mongo` | Transcript storage |
| `features/session/mongo` | Session state |
| **Streaming & Integration** | |
| `features/stream/pulse` | Pulse (Redis Streams) for real-time events |
| `features/policy/basic` | Policy engine for tool filtering |
| `registry` | Clustered registry service for cross-process toolset discovery and routing |
| `runtime/toolregistry` | Wire protocol and stream naming shared by registry providers, executors, and the clustered service |
| `runtime/mcp` | MCP callers (stdio, HTTP, SSE) for tool server integration |

---

## Bedrock Adapter Notes

The `features/model/bedrock` adapter keeps adaptive Claude thinking visible at
the `model.Client` boundary by explicitly requesting summarized reasoning
display. This preserves streamed `thinking` chunks across adaptive Claude model
revisions such as Opus 4.7, where the provider default changed to omit visible
reasoning text unless callers opt in.

---

## OpenAI Adapter Support

The `features/model/openai` adapter now targets the official `openai-go`
Responses API while preserving the same `model.Client` contract used by the
Bedrock and Anthropic adapters for the core Aura planner loop:

| Capability | OpenAI Responses API |
|------------|----------------------|
| Unary assistant text | Yes |
| Unary tool calls with provider IDs | Yes |
| Runtime-owned factory | Yes, via `Runtime.NewOpenAIModelClient(...)` |
| Explicit full transcript input | Yes, callers must pass the complete provider-ready transcript in `model.Request.Messages` |
| Transcript replay of assistant `tool_use` + user `tool_result` | Yes, for OpenAI-representable assistant turns; tool errors stay explicit |
| Streaming text | Yes |
| Streaming `tool_call_delta` + final `tool_call` | Yes |
| Streaming usage + stop chunks | Yes |
| Model-class routing (`default`, `high-reasoning`, `small`) | Yes |
| Structured output (`response_format: json_schema`) | Yes, but it cannot be combined with tools |
| Prompt cache options / cache checkpoints | Rejected explicitly |
| Thinking | Representable subset only: enable + configured `reasoning_effort`; budgeted or interleaved requests are rejected explicitly |

This is the acceptance target for Aura inference backends: planners continue to
talk to `model.Client`, while provider-specific details stay inside
`features/model/openai`.

Model adapters are now stateless at the transcript boundary: they do not look
up history from a `RunID`. Runtime-owned callers build the full transcript
explicitly and durable replay reconstructs that transcript from runlog
`transcript_messages_appended` records.

---

## Human-in-the-Loop

Pause runs for human review, resume when ready:

```go
// Pause a run
rt.PauseRun(ctx, interrupt.PauseRequest{
    RunID:  "run-123",
    Reason: "requires_approval",
})

// Resume after review
rt.ResumeRun(ctx, interrupt.ResumeRequest{
    RunID: "run-123",
    Notes: "Approved by reviewer",
})
```

The runtime updates run state and emits `run_paused`/`run_resumed` events for UI synchronization.

---

## Best Practices

**Design first** — Put all schemas in the DSL. Add examples and validations. Let codegen own the infrastructure.

**Never hand-encode** — Use generated codecs everywhere. Avoid `json.Marshal` for tool payloads.

**Keep planners focused** — Planners decide *what* (which tools or final answer). Tool implementations handle *how*.

**Compose with agent-as-tool** — Prefer nested agents over brittle cross-service contracts. Single history, unified debugging.

**Regenerate often** — DSL change → `goa gen` → lint/test → run. Never edit `gen/` manually.

---

## Documentation

| Guide | What You'll Learn |
|-------|-------------------|
| [Quickstart](https://goa.design/docs/2-goa-ai/quickstart/) | Installation and first agent in 10 minutes |
| [DSL Reference](https://goa.design/docs/2-goa-ai/dsl-reference/) | Complete DSL: agents, toolsets, policies, MCP |
| [Runtime](https://goa.design/docs/2-goa-ai/runtime/) | Plan/execute loop, engines, memory stores |
| [Toolsets](https://goa.design/docs/2-goa-ai/toolsets/) | Service-backed tools, transforms, executors |
| [Agent Composition](https://goa.design/docs/2-goa-ai/agent-composition/) | Agent-as-tool, run trees, streaming topology |
| [MCP Integration](https://goa.design/docs/2-goa-ai/mcp-integration/) | MCP servers, transports, generated wrappers |
| [Registry](https://goa.design/docs/2-goa-ai/registry/) | Clustered toolset discovery and invocation |
| [Production](https://goa.design/docs/2-goa-ai/production/) | Temporal setup, streaming UI, model providers |

**In-repo references:**
- [`docs/runtime.md`](docs/runtime.md) — Runtime architecture deep-dive
- [`docs/dsl.md`](docs/dsl.md) — DSL design patterns
- [`docs/overview.md`](docs/overview.md) — System overview

---

## Requirements

- **Go 1.24+**
- **Goa v3.22.2+** — `go install goa.design/goa/v3/cmd/goa@latest`
- **Temporal** (optional) — For durable execution in production
- **MongoDB** (optional) — Default memory/session/run event log store implementation
- **Redis** (optional) — For Pulse streaming and registry clustering

---

## Contributing

Issues and PRs welcome! Include a Goa design, failing test, or clear reproduction steps. See [`AGENTS.md`](AGENTS.md) for repository guidelines.

---

## License

MIT License © Raphael Simon & [Goa community](https://goa.design).

---

<p align="center">
  <i>Build agents that are a joy to develop and a breeze to operate.</i>
</p>
