# Goa-AI Runtime Reference

The goa-ai runtime is the execution engine that turns your agent designs into running
systems. It coordinates workflows, planners, tools, memory, streaming, and policies
into a cohesive whole. This document explains how the runtime works, how to configure
it, and how the generated code plugs in.

## When to Use This Guide

Read this guide when you need to:

- Bootstrap a runtime for your agents
- Understand the plan → execute → resume loop
- Configure policy enforcement, memory, and streaming
- Implement custom planners or tool executors
- Debug agent behavior or performance issues

For design-time DSL concepts, see [`docs/dsl.md`](dsl.md). For a high-level system
overview, see [`docs/overview.md`](overview.md).

---

## Mental Model

The runtime operates on three layers:

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Application Layer                            │
│  Services call generated clients to start runs and stream events    │
└────────────────────────────────┬────────────────────────────────────┘
                                 │
┌────────────────────────────────▼────────────────────────────────────┐
│                         Runtime Layer                               │
│  Orchestrates: Planners ↔ Tools ↔ Memory ↔ Hooks ↔ Policy           │
└────────────────────────────────┬────────────────────────────────────┘
                                 │
┌────────────────────────────────▼────────────────────────────────────┐
│                         Engine Layer                                │
│  Provides durable execution: Temporal, in-memory, or custom         │
└─────────────────────────────────────────────────────────────────────┘
```

**Key concepts:**

| Concept | Purpose |
|---------|---------|
| **Runtime** | Central registry and coordinator. Holds agents, toolsets, models, hooks, and stores. |
| **Engine** | Workflow backend (Temporal or in-memory). Provides durable execution, activities, and signals. |
| **Planner** | Decision-maker. Analyzes messages and returns tool calls or a final response. |
| **Toolset** | Collection of tools with shared execution logic. Generated from DSL or registered manually. |
| **Completion** | Service-owned typed direct assistant output. Generated under `gen/<service>/completions` with unary and streaming helpers backed by generated codecs. |
| **Hooks** | Internal event bus. Publishes lifecycle events for memory, streaming, and telemetry. |
| **Stream** | External event delivery. Transforms hook events into client-facing updates (SSE, WebSocket, Pulse). |

---

## Quick Start

### Minimal Example

```go
package main

import (
    "context"
    "fmt"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/runtime"
)

func main() {
    // 1. Create runtime (in-memory engine by default)
    rt := runtime.New()

    // 2. Register agent with a planner
    if err := chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{
        Planner: &MyPlanner{},
    }); err != nil {
        panic(err)
    }

    // 3. Create typed client and run
    client := chat.NewClient(rt)
    out, err := client.Run(context.Background(), "session-1", []*model.Message{{
        Role:  model.ConversationRoleUser,
        Parts: []model.Part{model.TextPart{Text: "Hello!"}},
    }})
    if err != nil {
        panic(err)
    }

    fmt.Println("Response:", out.Final)
}
```

### Production Configuration

```go
func main() {
    // Temporal engine for durable execution
    temporalEng, _ := temporal.NewWorker(temporal.Options{
        ClientOptions: &client.Options{HostPort: "temporal:7233"},
        WorkerOptions: temporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
    })
    defer temporalEng.Close()

    // MongoDB stores for persistence
    mongoClient := newMongoClient()
    memStore := memorymongo.New(mongoClient)

    // Pulse sink for real-time streaming
    pulseSink, _ := pulse.NewSink(pulse.Options{Client: newPulseClient()})

    // Construct runtime with all features
    rt := runtime.New(
        runtime.WithEngine(temporalEng),
        runtime.WithMemoryStore(memStore),
        runtime.WithStream(pulseSink),
        runtime.WithPolicy(basicpolicy.New()),
        runtime.WithLogger(telemetry.NewClueLogger()),
        runtime.WithMetrics(telemetry.NewClueMetrics()),
        runtime.WithTracer(telemetry.NewClueTracer()),
    )

    // Register toolsets first, then agents, then seal registration.
    if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
        Planner:      newChatPlanner(),
        HistoryModel: smallModelClient, // for history compression
    }); err != nil {
        panic(err)
    }
    if err := rt.Seal(ctx); err != nil {
        panic(err)
    }

    // Workers poll and execute; clients submit runs from anywhere
}
```

---

## Typed Direct Completions

Not every structured model interaction should be modeled as a tool call. When a
service needs a typed final assistant answer, declare `Completion(...)` in the
DSL and regenerate.

`goa gen` emits a service-owned package at `gen/<service>/completions` with:

- result schemas and typed result/union types
- generated JSON codecs and validation helpers
- typed `completion.Spec` values
- generated `Complete<Name>(ctx, client, req)` helpers
- generated `StreamComplete<Name>(ctx, client, req)` and `Decode<Name>Chunk(chunk)` helpers

Services may declare completions without declaring any `Agent(...)`. Agent
quickstart/example scaffolding is emitted only for services that actually own
agents.

Those helpers clone the request, attach provider-neutral `StructuredOutput`,
call the underlying `model.Client`, and decode the canonical typed payload
through the generated codec:

```go
resp, err := taskcompletion.CompleteDraftFromTranscript(ctx, modelClient, &model.Request{
    Messages: []*model.Message{{
        Role:  model.ConversationRoleUser,
        Parts: []model.Part{model.TextPart{Text: "Create a startup investigation task."}},
    }},
})
if err != nil {
    panic(err)
}

fmt.Println(resp.Value.Name)
```

Streaming completions stay on the raw `model.Streamer` surface and decode the
final canonical `completion` chunk only:

```go
stream, err := taskcompletion.StreamCompleteDraftFromTranscript(ctx, modelClient, &model.Request{
    Messages: []*model.Message{{
        Role:  model.ConversationRoleUser,
        Parts: []model.Part{model.TextPart{Text: "Create a startup investigation task."}},
    }},
})
if err != nil {
    panic(err)
}
defer stream.Close()

for {
    chunk, err := stream.Recv()
    if errors.Is(err, io.EOF) {
        break
    }
    if err != nil {
        panic(err)
    }
    value, ok, err := taskcompletion.DecodeDraftFromTranscriptChunk(chunk)
    if err != nil {
        panic(err)
    }
    if ok {
        fmt.Println(value.Name)
    }
}
```

Typed completion helpers are intentionally strict:

- Unary helpers accept unary requests only.
- Completion names are validated at the DSL boundary: 1-64 ASCII characters,
  letters/digits/`_`/`-` only, and must start with a letter or digit.
- Unary and streaming helpers reject tool-enabled requests and caller-supplied `StructuredOutput`.
- Streaming providers emit `completion_delta*` preview fragments plus exactly one canonical `completion` chunk, or reject the request explicitly.
- `Decode<Name>Chunk` ignores preview chunks and decodes only the final `completion`.
- Completion streams stay on the direct `model.Streamer` path; do not route them through planner streaming helpers, which are for assistant transcript text/tool execution events.
- Providers that do not implement structured output surface `model.ErrStructuredOutputUnsupported`.
- Generated schemas are canonical and provider-neutral; provider adapters may normalize them to a supported subset, but must fail explicitly when they cannot preserve the declared contract.

---

## OpenAI Adapter Matrix

The `features/model/openai` adapter now targets the official `openai-go`
Responses API while satisfying the core `model.Client` contract expected by
planner and runtime streaming:

| Capability | Status |
|------------|--------|
| Unary assistant text | Supported |
| Unary tool calls with provider-issued IDs | Supported |
| Runtime-owned factory | Supported via `Runtime.NewOpenAIModelClient(...)` |
| Explicit full transcript input | Supported; callers pass the complete provider-ready transcript in `model.Request.Messages` |
| Assistant `tool_use` + user `tool_result` transcript replay | Supported for OpenAI-representable assistant turns; tool-result errors stay explicit |
| Streaming text | Supported |
| Streaming `tool_call_delta` and final `tool_call` | Supported |
| Streaming usage and stop chunks | Supported |
| Model-class routing (`default`, `high-reasoning`, `small`) | Supported |
| Structured output (`completion_delta` + final `completion`) | Supported via OpenAI `json_schema` response format, but not in combination with tools |
| Cache options / cache checkpoints | Rejected explicitly |
| Thinking | Only the representable subset is supported: `Thinking.Enable` may map to configured OpenAI `reasoning_effort`; budgeted or interleaved thinking requests fail fast |

This is the intended migration seam for Aura-style inference backends: swap the
provider adapter, keep planners and runtime flow unchanged.

Model adapters are stateless at the transcript boundary. They never rehydrate
history from a `RunID`; runtime-owned callers must supply the full transcript,
and durable recovery rebuilds that transcript from runlog
`transcript_messages_appended` records.

---

## Runtime Configuration

### Construction Options

Create a runtime using `runtime.New()` with functional options:

```go
rt := runtime.New(
    runtime.WithEngine(engine),          // Workflow backend (required for production)
    runtime.WithMemoryStore(store),      // Transcript persistence
    runtime.WithPromptStore(promptStore),// Scoped prompt overrides
    runtime.WithStream(sink),            // Real-time event streaming
    runtime.WithPolicy(engine),          // Policy enforcement
    runtime.WithHooks(bus),              // Custom event bus (rare)
    runtime.WithLogger(logger),          // Structured logging
    runtime.WithMetrics(metrics),        // Counter/histogram recording
    runtime.WithTracer(tracer),          // Distributed tracing
    runtime.WithWorker(agentID, cfg),    // Per-agent queue placement
)
```

When options are omitted, the runtime uses sensible defaults:

| Option | Default |
|--------|---------|
| Engine | In-memory (synchronous, non-durable) |
| MemoryStore | None (transcripts not persisted) |
| PromptStore | None (baseline prompt specs only, no scoped overrides) |
| Stream | None (no external event delivery) |
| Policy | None (all tools allowed, caps from agent registration) |
| Hooks | In-process bus |
| Logger/Metrics/Tracer | No-op implementations |

`runtime.WithWorker(...)` is intentionally narrow: it controls agent placement
(`Queue`) only. Semantic planner and tool attempt budgets come from the DSL
(`RunPolicy.Timing`) or per-run overrides (`runtime.WithTiming(...)`). If you
use the Temporal engine and need queue-wait or liveness tuning, configure those
mechanics on `temporal.Options.ActivityDefaults` when constructing the engine.

### Prompt Registry and Overrides

The runtime always initializes `Runtime.PromptRegistry`. Prompt management has two layers:

- **Baseline specs**: register immutable `prompt.PromptSpec` definitions in memory.
- **Scoped overrides**: optionally resolve `org/facility/session` overrides through `prompt.Store`
  (`runtime.WithPromptStore(...)`).

```go
import (
    promptmongo "goa.design/goa-ai/features/prompt/mongo"
    clientmongo "goa.design/goa-ai/features/prompt/mongo/clients/mongo"
    "goa.design/goa-ai/runtime/agent/prompt"
)

mongoClient, _ := clientmongo.New(clientmongo.Options{
    Client:     rawMongoClient,
    Database:   "aura",
    Collection: "prompt_overrides",
})
promptStore, _ := promptmongo.NewStore(mongoClient)

rt := runtime.New(
    runtime.WithPromptStore(promptStore),
)

_ = rt.PromptRegistry.Register(prompt.PromptSpec{
    ID:       "aura.chat.system",
    AgentID:  "orchestrator.chat",
    Role:     prompt.PromptRoleSystem,
    Template: "You are {{ .AssistantName }}.",
})
```

Render prompts from planners through `PlannerContext.RenderPrompt(...)`. The result includes rendered
text and a versioned `PromptRef` for provenance.

### Two Deployment Patterns

**Worker process** — Registers agents and executes workflows:

```go
rt := runtime.New(runtime.WithEngine(temporalWorker))

// Register agents with planners
if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
    Planner: myPlanner,
}); err != nil {
    panic(err)
}

// Workers poll task queues and execute runs
```

**Client-only process** — Submits runs without local execution:

```go
rt := runtime.New(runtime.WithEngine(temporalClient))

// No registration needed; use generated client with route info
client := chat.NewClient(rt)
out, err := client.Run(ctx, "session-1", msgs)
```

The generated `NewClient` function embeds the route (workflow name, task queue) so
client-only processes can submit runs to remote workers.

---

## The Plan → Execute → Resume Loop

Every agent run follows this lifecycle:

```
Start ──► PlanStart ──► Tool Calls? ──► Execute Tools ──► PlanResume ──► ...
                │                                              │
                │                                              │
                └──► Final Response ◄──────────────────────────┘
```

1. **Start** — `client.Run()` or `client.Start()` creates a workflow
2. **PlanStart** — Planner receives messages and decides: answer or call tools?
3. **Execute** — Tools run as activities (parallel by default)
4. **PlanResume** — Planner receives tool results and decides next step
5. **Repeat** — Loop continues until planner returns a `FinalResponse`

### Workflow Contracts

- **SessionID is required.** `Start` fails fast if `SessionID` is empty.
- **Agents must register before runs start.** Registration closes after the first
  run to maintain worker determinism.
- **Tool results flow through codecs.** The runtime decodes results centrally and
  provides typed values to planners and hooks.

### Tool payload codecs and defaults (Feature)

Tool payloads are decoded using a Goa‑style two‑step model:

1. **Decode JSON into a helper “decode‑body” type** with pointer fields, so the codec can
   distinguish **missing** from **zero** and return precise validation issues.
2. **Transform helper → final payload** using Goa’s `codegen.GoTransform`.

For tool payloads, the generated payload struct uses **default‑aware field shapes**:
optional primitives with defaults become **values** (non‑pointers). During step (2), Goa’s transform
generator injects defaults when helper fields are nil.

This is a hard codegen contract: any generated transforms that read tool payload fields must use
matching AttributeContext default semantics, or the generated code may contain invalid nil checks or
assignments and fail to compile.

See [`docs/tool_payload_defaults.md`](tool_payload_defaults.md) for the full contract.

---

## Planner Contract

Planners implement the decision logic for agents. The runtime invokes planners through
activities and feeds results back into the workflow loop.

### Interface

```go
type Planner interface {
    PlanStart(ctx context.Context, input *PlanInput) (*PlanResult, error)
    PlanResume(ctx context.Context, input *PlanResumeInput) (*PlanResult, error)
}
```

**PlanStart** receives the initial messages; **PlanResume** receives messages plus
recent tool results. Both return a `PlanResult` containing tool calls, a final
response, or an await request.

### PlanInput and PlanResumeInput

```go
type PlanInput struct {
    Messages   []*model.Message      // Conversation history
    RunContext run.Context           // Run-level identifiers and labels
    Agent      PlannerContext        // Runtime services (memory, models, reminders)
    Events     PlannerEvents         // Streaming event emitter
    Reminders  []reminder.Reminder   // Active system reminders
}

type PlanResumeInput struct {
    Messages    []*model.Message
    RunContext  run.Context
    Agent       PlannerContext
    Events      PlannerEvents
    ToolOutputs []*ToolOutput         // Results from previous tool calls
    Finalize    *Termination          // Non-nil when runtime forces finalization
    Reminders   []reminder.Reminder
}
```

Planners receive fully hydrated `ToolOutputs`, but the workflow/activity wire
format no longer carries raw tool payloads or result bodies inline.
`PlanActivityInput.ToolOutputs` ships tool-call references only, and the runtime
rehydrates `Payload`, `Result`, `ServerData`, and planner-visible result
metadata from the canonical run log inside `PlanResumeActivity` before invoking
the planner.

### PlanResult

```go
type PlanResult struct {
    ToolCalls     []ToolRequest    // Tools to execute (empty for final response)
    FinalResponse *FinalResponse   // Terminal assistant message
    Streamed      bool             // True if text was already streamed via Events
    Await         *Await           // Pause for clarification or external tools
    RetryHint     *RetryHint       // Guidance after tool failures
    Notes         []PlannerAnnotation
}
```

### PlannerContext

`PlannerContext` provides read-only access to runtime services:

```go
type PlannerContext interface {
    ID() agent.Ident                      // Agent identifier
    RunID() string                        // Current run identifier
    Memory() memory.Reader                // Read prior turn history
    Logger() telemetry.Logger             // Structured logging
    Metrics() telemetry.Metrics           // Counters and histograms
    Tracer() telemetry.Tracer             // Distributed tracing
    State() AgentState                    // Ephemeral per-run key-value store
    AdvertisedToolDefinitions() []*model.ToolDefinition // Runtime-filtered model-facing tools
    ModelClient(id string) (model.Client, bool)  // Raw LLM client lookup
    PlannerModelClient(id string) (planner.PlannerModelClient, bool) // Planner-scoped client with runtime-owned event emission
    RenderPrompt(ctx context.Context, id string, data any) (*prompt.PromptContent, error)
    AddReminder(r reminder.Reminder)      // Register backstage guidance
    RemoveReminder(id string)             // Clear a reminder
}
```

Use `AdvertisedToolDefinitions()` when constructing provider requests inside planners. The
runtime filters registered tool specs before the planner/model sees them and strips tag metadata
from the model-facing `ToolDefinition` values.

### PlannerEvents

`PlannerEvents` emits streaming updates that the runtime captures and publishes:

```go
type PlannerEvents interface {
    AssistantChunk(ctx context.Context, text string)
    PlannerThinkingBlock(ctx context.Context, block model.ThinkingPart)
    PlannerThought(ctx context.Context, note string, labels map[string]string)
    UsageDelta(ctx context.Context, usage model.TokenUsage)
}
```

---

## Streaming Planners

When using model streaming, planners now have two explicit integration styles.
Choose one per planner call.

### Option 1: PlannerModelClient (Recommended)

`PlannerContext.PlannerModelClient(id)` returns a planner-scoped client that owns
`AssistantChunk`, `PlannerThinkingBlock`, and `UsageDelta` emission. Its
`Stream(...)` method drains the underlying provider stream and returns a
`planner.StreamSummary`:

```go
func (p *MyPlanner) PlanResume(ctx context.Context, input *PlanResumeInput) (*PlanResult, error) {
    mc, ok := input.Agent.PlannerModelClient("bedrock")
    if !ok {
        return nil, errors.New("model not configured")
    }

    req := &model.Request{
        ModelClass: model.ModelClassHighReasoning,
        Messages:   input.Messages,
        Stream:     true,
    }

    sum, err := mc.Stream(ctx, req)
    if err != nil {
        return nil, err
    }
    if len(sum.ToolCalls) > 0 {
        return &PlanResult{ToolCalls: sum.ToolCalls}, nil
    }
    return &PlanResult{
        FinalResponse: &FinalResponse{
            Message: &model.Message{
                Role:  model.ConversationRoleAssistant,
                Parts: []model.Part{model.TextPart{Text: sum.Text}},
            },
        },
        Streamed: true, // Text was already streamed
    }, nil
}
```

This is the safest integration style because the planner-scoped client does not
expose a raw `model.Streamer`, so it cannot be combined accidentally with
`planner.ConsumeStream`.

### Option 2: ConsumeStream with Raw Client

When you want the raw `model.Client`, fetch it from `PlannerContext.ModelClient`
and pair it with `planner.ConsumeStream`:

```go
mc, ok := input.Agent.ModelClient("bedrock")
if !ok {
    return nil, errors.New("model not configured")
}
st, err := mc.Stream(ctx, req)
if err != nil {
    return nil, err
}
sum, err := planner.ConsumeStream(ctx, st, req, input.Events)
if err != nil {
    return nil, err
}
```

This helper drains the stream, emits events via `PlannerEvents`, and returns a
`StreamSummary` with accumulated text and tool calls.

Use the raw client path when you need full control over stream consumption or
want to bypass runtime-owned event emission entirely and manage `input.Events`
yourself.

---

## Tool Execution

### Tool Payload and Result Flow

1. **Model emits tool call** — Provider adapter produces `model.ToolCall` with
   `json.RawMessage` payload
2. **Planner returns ToolRequest** — Payload stays as `json.RawMessage`
3. **Runtime decodes payload** — Uses generated codecs to validate and decode
4. **Executor runs tool** — Receives typed or raw payload depending on configuration
5. **Runtime encodes result** — Uses generated codecs for consistency
6. **Planner receives ToolResult** — Gets typed result via `ToolResult.Result`

### ToolsetRegistration

Toolsets bundle execution logic for a group of tools:

```go
type ToolsetRegistration struct {
    Name        string                     // Qualified identifier (service.toolset)
    Description string                     // Human-readable context
    Metadata    policy.ToolMetadata        // Policy metadata
    Execute     func(ctx, *ToolRequest) (*ToolResult, error)  // Dispatcher
    Specs       []tools.ToolSpec           // JSON codecs and schemas
    TaskQueue   string                     // Optional queue override
    Inline      bool                       // Execute in workflow context
    CallHints   map[tools.Ident]*template.Template   // Tool call DisplayHint templates (typed payload only)
    ResultHints map[tools.Ident]*template.Template   // Tool result preview templates (typed result only)
    PayloadAdapter func(...)               // Pre-decode transformation
    ResultAdapter  func(...)               // Post-encode transformation
    AgentTool   *AgentToolConfig           // Agent-as-tool configuration
}
```

### Tool Call Display Hints (DisplayHint)

The runtime can surface user-facing hints for tool calls (for example in UIs) via the `DisplayHint` field on
hook + stream events.

Contract:

- Hook constructors do not render hints. Tool call scheduled events default to `DisplayHint==""`.
- The runtime may enrich and persist a **durable default** hint at publish time by decoding the typed tool
  payload using generated codecs and executing the `CallHintTemplate` (if registered).
- When typed decoding fails or no template is registered, the runtime leaves `DisplayHint` empty. Hints are
  never rendered against raw JSON bytes.
- If a producer explicitly sets `DisplayHint` (non-empty) before publishing the hook event, the runtime treats
  it as authoritative and does not overwrite it.

For per-consumer wording changes, configure `runtime.WithHintOverrides` on the runtime. Overrides take precedence
over DSL-authored templates for streamed `tool_start` events.

### Tool Implementation Patterns

**Method-backed tools** — Generated from `BindTo` DSL:

```go
// Generated code maps tool payloads to service method calls
reg := helpers.NewHelpersToolsetRegistration(serviceClient)
rt.RegisterToolset(reg)
```

### Registry-Routed Provider Execution (Service-Side)

Goa-AI supports cross-process tool invocation via the **Internal Tool Registry**. In this mode:

- The registry validates payload JSON against the tool schema and publishes tool calls to a deterministic Pulse stream: `toolset:<toolsetID>:requests`
- A **provider loop** runs inside the toolset-owning service process, subscribes to the toolset stream, executes the tool, and publishes the result to `result:<toolUseID>`

For method-backed service toolsets, codegen emits a provider adapter at:

- `gen/<service>/toolsets/<toolset>/provider.go`

That generated provider implements a dispatcher that decodes the tool payload JSON using generated codecs, adapts into the Goa method payload (via generated transforms), calls the bound method, and re-encodes the tool result JSON together with any declared server-data (optional observer-facing server-data and always-on server-only metadata).

To run it, wire the generated provider into the runtime provider loop:

```go
handler := toolsetpkg.NewProvider(serviceImpl)
go func() {
    err := toolprovider.Serve(ctx, pulseClient, toolsetID, handler, toolprovider.Options{
        Pong: func(ctx context.Context, pingID string) error {
            return registryClient.Pong(ctx, &registry.PongPayload{
                PingID:  pingID,
                Toolset: toolsetID,
            })
        },
    })
    if err != nil {
        panic(err)
    }
}()
```

This integration is intentionally split:

- **Registry gateway**: validates payloads, tracks provider health, creates per-call result streams, and returns `tool_use_id`
- **Service provider loop**: executes tools using the generated provider adapters and publishes results

### Registry-Routed Execution (Agent/Consumer Side)

On the consumer side (an agent calling registry-routed toolsets), the runtime needs a `ToolCallExecutor` that:

- calls the registry gateway to publish the tool request and get a `(tool_use_id, result_stream_id)`, then
- subscribes to the per-call result stream and decodes the result using the compiled tool specs/codecs.

Goa-AI provides a reusable executor implementation in `runtime/toolregistry/executor` that implements `runtime.ToolCallExecutor`:

```go
import (
    toolregexec "goa.design/goa-ai/runtime/toolregistry/executor"
)

exec := toolregexec.New(registryClient, pulseClient, specs)

// Use exec.Execute as the executor for registry-backed toolsets.
```

The registry wire protocol and deterministic stream IDs are defined in `runtime/toolregistry`:

- Toolset request stream: `toolset:<toolsetID>:requests`
- Per-call result stream: `result:<toolUseID>`

### Registry discovery & catalog sync

If you need runtime discovery of toolsets and schemas (for example, tool
catalogs that change without a `goa gen`), use the generated agent-side
registry client packages under `gen/<service>/registry/<name>/`.

Those generated clients own the consumer-side discovery flow. The standalone
clustered registry service implementation lives under `goa-ai/registry`, and
the shared Pulse wire protocol lives under `goa-ai/runtime/toolregistry`.

**Inline tools** — Custom executor implementation:

```go
reg := runtime.ToolsetRegistration{
    Name: "myservice.helpers",
    Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
        // Decode payload, execute logic, return result
    },
    Specs: []tools.ToolSpec{...},
}
rt.RegisterToolset(reg)
```

**Agent-as-tool** — Nested agent execution:

```go
reg := runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
    AgentID: agent.Ident("service.nested"),
    Route:   runtime.AgentRoute{...},
    // Optional per-tool prompts/templates
})
```

### ToolCallMeta

Executors receive explicit per-call metadata:

```go
type ToolCallMeta struct {
    RunID            string  // Workflow execution identifier
    SessionID        string  // Logical session grouping
    TurnID           string  // Conversational turn identifier
    ToolCallID       string  // Unique tool invocation ID
    ParentToolCallID string  // Parent tool call (for agent-as-tool)
}
```

### Optional server-data (reserved `"server_data"` payload field)

Tools can optionally produce **observer-facing server-data** (often projected into UI artifacts) that is never sent to model providers.
The runtime supports a per-call optional server-data toggle via a reserved top-level tool payload field:

- `{"server_data":"auto"}` — use the tool default
- `{"server_data":"on"}` — enable optional server-data (when the tool declares it)
- `{"server_data":"off"}` — disable optional server-data for this call

The runtime strips the reserved `"server_data"` field from the execution payload before decoding, and records the
normalized value on the tool call metadata (`ServerDataMode`). Tool payload schemas must not define a top-level
property named `"server_data"`.

### Bounded Results

Tools that return partial views of larger datasets should use the `BoundedResult`
DSL helper. This enforces a canonical bounded-result contract:
bounded tools declare their contract in `tools.ToolSpec.Bounds`, successful
executions must populate `planner.ToolResult.Bounds`, and the runtime projects
the canonical bounds fields (`returned`, `total`, `truncated`,
`refinement_hint`, and optional `next_cursor`) into the emitted result JSON and
hook/stream payloads.

The runtime enforces one strict contract across all result ingress paths
(regular execution and externally provided await results):

- unbounded tools must not return bounds metadata,
- error tool results must not return bounds metadata,
- successful bounded results must include bounds metadata,
- when `truncated=true`, bounds must include either `next_cursor` or
  `refinement_hint`.

```go
type Bounds struct {
    Returned       int     // Items in this response
    Total          *int    // Total items available (when known)
    Truncated      bool    // True if limits were applied
    RefinementHint string  // Guidance for narrowing queries
}
```

The runtime surfaces bounds via `ToolResult.Bounds`, encoded `tool_result` JSON,
result-hint templates under `.Bounds`, hook events, and stream events. Services
own truncation logic; the runtime only propagates and projects what tools
report.

Transcript-facing tool results use a stricter provider contract than execution
boundaries:

- canonical raw bytes live in `ToolOutput.Result`, `ToolResultReceivedEvent.ResultJSON`,
  and durable memory-event `result_json`,
- `model.ToolResultPart.Content` carries semantic provider-facing content only:
  decoded JSON-compatible values on success or plain error text with `IsError=true`,
- oversized successful transcript content projects to an explicit omission object:
  `{"omitted":true,"reason":"size_limit","preview":"...","bounds":{...}}`.

For method-backed `BindTo` tools, the bound service method result still needs to
carry the canonical bounded fields so the generated executor can build
`planner.ToolResult.Bounds` before runtime projection. Explicit tool-facing
`Return(...)` shapes must not duplicate those canonical fields. Within the bound
method result, only `returned` and `truncated` may be required; `total`,
`refinement_hint`, and `next_cursor` remain optional and are omitted from emitted
JSON whenever runtime bounds omit them. `BoundedResult(...)` still owns the
tool-facing contract exposed to models.

When a service boundary must assemble canonical result JSON outside
`ExecuteToolActivity` itself, use `runtime.EncodeCanonicalToolResult(...)`
instead of calling the generated result codec and bounded-result projection
helpers separately.

---

## Agent-as-Tool Composition

Agents can expose tools via `Export` blocks and consume them via `Use`. When invoked,
nested agents execute as child workflows with their own run IDs and event streams.

### How It Works

1. Parent planner requests tool (e.g., `"service.analysis.analyze"`)
2. Runtime identifies it as an agent-tool via `ToolSpec.IsAgentTool`
3. Runtime starts child workflow using `AgentToolConfig.Route`
4. Child agent executes its own plan/execute loop
5. Runtime returns a parent `ToolResult` derived from the child run output (final text and/or finalizer output, plus aggregated telemetry). **Artifacts are not propagated to the parent tool result**; they remain attached to the child tool events.
6. `ChildRunLinked` event links parent and child for streaming

### Configuration

```go
reg := runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
    AgentID:         agent.Ident("service.data-analyst"),
    Route:           runtime.AgentRoute{
        ID:               agent.Ident("service.data-analyst"),
        WorkflowName:     "DataAnalystWorkflow",
        DefaultTaskQueue: "orchestrator.data-analyst",
    },
    SystemPrompt:    "You are a data analysis expert.",
    AgentToolContent: runtime.AgentToolContent{
        Templates: compiledTemplates, // Per-tool user message templates (optional)
        Texts:     textMessages,      // Alternative to templates (optional)
    },
    JSONOnly:        true,                // Return structured results
    Finalizer:       myFinalizer,         // Custom result aggregation
})
```

### Per-Tool Content

Configure how tool payloads become the nested agent's initial user message.
When you do not configure consumer-side content, the runtime uses a deterministic
default: the canonical JSON tool payload bytes (verbatim) as the nested user
message.

```go
// Plain text for all tools
runtime.WithTextAll(toolIDs, "Process this: {{ . }}")

// Template for specific tool
runtime.WithTemplate(toolID, compiledTemplate)

// PromptSpec for a tool (optional; payload-only)
runtime.WithPromptSpec(toolID, "my.prompt.id")

// Custom prompt builder
cfg.Prompt = func(id tools.Ident, payload any) string {
    return fmt.Sprintf("Handle %s request: %v", id.Tool(), payload)
}
```

### Finalizers

Finalizers aggregate child results into the parent tool result:

```go
// Pass-through: use JSONOnly aggregation
runtime.PassThroughFinalizer()

// Tool-based: call a dedicated aggregation tool
runtime.ToolResultFinalizer(tools.Ident("helpers.aggregate"), func(ctx, input) (any, error) {
    return map[string]any{"children": input.Children}, nil
})

// Custom: full control over aggregation
runtime.FinalizerFunc(func(ctx, input FinalizerInput) (ToolResult, error) {
    // Build result from input.Children
    return planner.ToolResult{Result: aggregated}, nil
})
```

---

## Human-in-the-Loop

Runs can pause and resume via interrupt signals, enabling approval workflows,
clarification requests, and external tool integration.

### Pause and Resume

```go
// Pause a run (from outside the workflow)
err := rt.PauseRun(ctx, interrupt.PauseRequest{
    RunID:       "run-123",
    Reason:      "human_review",
    RequestedBy: "policy-engine",
})

// Resume after approval
err := rt.ResumeRun(ctx, interrupt.ResumeRequest{
    RunID:       "run-123",
    Notes:       "Approved by admin",
    Messages:    additionalMessages, // Optional
})
```

### Clarification Requests

Planners can request missing information:

```go
return &planner.PlanResult{
    Await: &planner.Await{
        Clarification: &planner.AwaitClarification{
            ID:            "clarify-device",
            Question:      "Which device should I configure?",
            MissingFields: []string{"device_id"},
        },
    },
}
```

The runtime pauses the workflow and emits an `AwaitClarification` event. Callers
respond via:

```go
err := rt.ProvideClarification(ctx, interrupt.ClarificationAnswer{
    RunID:  "run-123",
    ID:     "clarify-device",
    Answer: "Device ID is ABC-123",
})
```

### External Tools

Planners can request tools that execute out-of-band:

```go
return &planner.PlanResult{
    Await: &planner.Await{
        ExternalTools: &planner.AwaitExternalTools{
            ID: "external-1",
            Items: []planner.AwaitToolItem{{
                Name:       tools.Ident("external.fetch"),
                ToolCallID: "tc-ext-1",
                Payload:    json.RawMessage(`{"url":"..."}`),
            }},
        },
    },
}
```

Callers provide results via:

```go
err := rt.ProvideToolResults(ctx, &api.ToolResultsSet{
    RunID: "run-123",
    ID:    "external-1",
    Results: []*api.ProvidedToolResult{
        {
            ToolCallID: "toolcall-1",
            Name:       tools.Ident("chat.ask_question.ask_question"),
            // Contract: canonical JSON bytes matching the tool's Return schema.
            Result: json.RawMessage(`{"answers":[{"question_id":"...","selected_ids":["approve"]}]}`),
        },
    },
})
```

Provided tool results are strict boundary inputs:

- each item must be exactly one of: `Error` or non-null `Result`,
- if the tool is bounded and successful, `Bounds` must be present and satisfy
  bounded-result invariants.

Those rules apply only at execution/history boundaries. Once the runtime projects
tool output into transcript messages, models never see raw `Result` bytes or
structured Go error values.

### Tool Confirmation (Design-Time + Runtime Overrides)

Goa-AI supports **runtime-enforced** confirmation gates for sensitive tools.

There are two ways to enable confirmation:

- **Design-time (recommended, common case):** declare `Confirmation(...)` inside a tool DSL.
  Codegen stores the confirmation policy in the generated `tools.ToolSpec.Confirmation`.
- **Runtime (dynamic/override):** supply `runtime.WithToolConfirmation(...)` when constructing the
  runtime. This can require confirmation for additional tools and/or override the design-time behavior
  for specific tool IDs.

At execution time, the workflow:

- Emits an out-of-band confirmation request (using `AwaitConfirmation`) before executing the
  target tool call.
- Waits for a user approval/denial decision.
- Executes the tool only when approved.
- When denied, synthesizes a **schema-compliant** tool result (so the transcript remains valid and
  the planner can react to the denial deterministically).

**Confirmation protocol**

The runtime uses a runtime-owned confirmation protocol to obtain an explicit approval/denial
decision before executing a confirmed tool.

- **Await payload** (hook + stream event):

  ```json
  {
    "id": "...",
    "title": "...",
    "prompt": "...",
    "tool_name": "atlas.commands.change_setpoint",
    "tool_call_id": "toolcall-1",
    "payload": { "...": "canonical tool arguments (JSON)" }
  }
  ```

- **Provide decision** (using `ProvideConfirmation`):

  ```go
  err := rt.ProvideConfirmation(ctx, interrupt.ConfirmationDecision{
      RunID:       "run-123",
      ID:         "await-1",
      Approved:    true,              // or false
      RequestedBy: "user:123",        // optional, for audit
      Labels:      map[string]string{"source": "front-ui"},
      Metadata:    map[string]any{"ticket_id": "INC-42"},
  })
  ```

Consumers should treat confirmation as a **runtime protocol**, not as a user-defined tool:

- Use the accompanying `RunPaused` reason (`await_confirmation`) to decide when to display a confirmation UI.
- Do not couple UI behavior to a specific confirmation tool name; treat it as an internal transport detail.

This keeps the runtime generic: any UI/system can implement a compatible confirmation transport.

### Tool authorization events

When a decision is provided via `ProvideConfirmation`, the runtime emits a first-class authorization event:

- **Hook event**: `hooks.ToolAuthorization`
- **Stream event type**: `tool_authorization`

This event is emitted exactly once per confirmed tool call and captures the durable authorization record:

- `tool_name`: the tool being authorized
- `tool_call_id`: the tool call identifier
- `approved`: true/false decision
- `summary`: deterministic runtime-rendered summary (derived from the confirmation prompt)
- `approved_by`: copied from `interrupt.ConfirmationDecision.RequestedBy` and intended to be a stable principal identifier (for example, `user:<id>`)

The event is emitted immediately after the decision is received:

- **Approved**: emitted before the tool executes.
- **Denied**: emitted before the denied tool result is synthesized.

Consumers (UIs, audit stores, session recorders) should rely on `tool_authorization` for “who/when/what” rather than inferring authorization from tool results.

**Runtime validation**

The runtime treats confirmation as a boundary and validates:

- The confirmation `ID` matches the pending await identifier when provided.
- The decision object is well-formed (non-empty `RunID`, boolean `Approved` value).

Notes:

- Confirmation templates (`PromptTemplate` and `DeniedResultTemplate`) are Go `text/template` strings
  executed with `missingkey=error`. In addition to the standard template functions (e.g. `printf`),
  Goa-AI provides:
  - `json v` → JSON encodes `v` (useful for optional pointer fields or embedding structured values).
  - `quote s` → returns a Go-escaped quoted string (like `fmt.Sprintf("%q", s)`).

---

## Hooks and Streaming

### Hook Bus

The runtime publishes events to an internal bus (`hooks.Bus`). Default subscribers
handle memory persistence and stream forwarding.

**Determinism note:** When using a durable workflow engine (e.g., Temporal),
workflow code must be deterministic and must not trigger external I/O. The
runtime therefore routes workflow-emitted records through a dedicated record
activity (`runtime.record_event`), which persists canonical run-log records and
fans out hook-backed records outside the workflow thread. Activities and other
non-workflow code publish directly.

**Event types:**

| Event | When |
|-------|------|
| `RunStarted` | Run begins |
| `RunCompleted` | Run finishes (success, failed, canceled) |
| `RunPaused` / `RunResumed` | Human-in-the-loop transitions |
| `RunPhaseChanged` | Phase transitions (planning, executing_tools, etc.) |
| `PromptRendered` | Runtime resolves and renders a prompt spec |
| `ToolCallScheduled` | Tool activity scheduled |
| `ToolResultReceived` | Tool completes |
| `ToolCallUpdated` | Parent tool discovers more children |
| `AssistantMessage` | Final assistant response |
| `PlannerNote` / `ThinkingBlock` | Planner reasoning |
| `AwaitClarification` / `AwaitExternalTools` | Pause requests |
| `PolicyDecision` | Policy evaluation result |
| `Usage` | Token usage report |
| `ChildRunLinked` | Agent-as-tool child run link |

### Custom Subscribers

```go
sub := hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
    switch e := evt.(type) {
    case *hooks.ToolResultReceivedEvent:
        log.Printf("Tool %s completed in %v", e.ToolName, e.Duration)
    }
    return nil
})

subscription, _ := rt.Bus.Register(sub)
defer subscription.Close()
```

### Stream Sink

The `stream.Sink` interface delivers client-facing events:

```go
type Sink interface {
    Send(ctx context.Context, event Event) error
    Close(ctx context.Context) error
}
```

**Stream event types:**

| Event | Payload |
|-------|---------|
| `prompt_rendered` | `PromptRenderedPayload` (`prompt_id`, `version`, `scope`) |
| `tool_start` | `ToolStartPayload` (tool_call_id, tool_name, payload) |
| `tool_end` | `ToolEndPayload` (result, error, duration, telemetry) |
| `tool_update` | `ToolUpdatePayload` (expected_children_total) |
| `assistant_reply` | `AssistantReplyPayload` (text) |
| `planner_thought` | `PlannerThoughtPayload` (note, thinking blocks) |
| `await_clarification` | `AwaitClarificationPayload` |
| `await_external_tools` | `AwaitExternalToolsPayload` |
| `usage` | `UsagePayload` (input_tokens, output_tokens) |
| `workflow` | `WorkflowPayload` (phase, status, error_kind, retryable, error, debug_error) |
| `child_run_linked` | `ChildRunLinkedPayload` (child run link) |

### Stream Profiles

Control which events reach each audience:

```go
// All events, child runs linked
stream.DefaultProfile()

// User chat view (default for most UIs)
stream.UserChatProfile()

// Debug view (all events; child runs linked)
stream.AgentDebugProfile()

// Metrics only (usage, workflow)
stream.MetricsProfile()
```

### Workflow payload contract (phases, terminal status, and errors)

The runtime emits:

- `RunPhaseChanged` hook events for **non-terminal** phase transitions (`planning`, `executing_tools`, `synthesizing`, etc.)
- a single `RunCompleted` hook event per run for the **terminal** lifecycle state

The stream subscriber translates these into `workflow` stream events:

- **Non-terminal updates** (from `RunPhaseChanged`): `phase` only.
- **Terminal update** (from `RunCompleted`): `status` + terminal `phase`.

Terminal status mapping:

- `status="success"` → `phase="completed"`
- `status="failed"` → `phase="failed"`
- `status="canceled"` → `phase="canceled"`

Cancellation is not an error:

- For `status="canceled"`, the workflow payload must not include a user-facing `error`.

Failures are structured:

- For `status="failed"`, the workflow payload includes:
  - `error_kind`: stable classifier (provider kinds like `rate_limited`, `unavailable`, or runtime kinds like `timeout`/`internal`)
  - `retryable`: whether retrying may succeed without changing input
  - `error`: **user-safe** message suitable for direct display
  - `debug_error`: raw error string for logs/diagnostics (not for UI)

## Policy Enforcement

Policy engines decide which tools are available each turn and enforce caps.

### Policy Engine Interface

```go
type Engine interface {
    Decide(ctx context.Context, input Input) (Decision, error)
}
```

**Input:**

```go
type Input struct {
    RunContext    run.Context        // Run identifiers and labels
    Tools         []ToolMetadata     // Candidate tools
    RetryHint     *RetryHint         // Planner guidance after failures
    RemainingCaps CapsState          // Current execution budgets
    Requested     []tools.Ident      // Explicitly requested tools
    Labels        map[string]string  // Context labels
}
```

**Decision:**

```go
type Decision struct {
    AllowedTools []tools.Ident      // Tools permitted this turn
    Caps         CapsState          // Updated execution budgets
    DisableTools bool               // Force final response
    Labels       map[string]string  // Labels to propagate
    Metadata     map[string]any     // Audit trail data
}
```

### Caps State

```go
type CapsState struct {
    MaxToolCalls                        int
    RemainingToolCalls                  int
    MaxConsecutiveFailedToolCalls       int
    RemainingConsecutiveFailedToolCalls int
    ExpiresAt                           time.Time
}
```

### Per-Run Policy Overrides

Callers can override policy for specific runs:

```go
client.Run(ctx, "session-1", msgs,
    runtime.WithRunMaxToolCalls(5),
    runtime.WithRunTimeBudget(2*time.Minute),
    runtime.WithRestrictToTool(tools.Ident("helpers.search")),
    runtime.WithTagPolicyClauses([]runtime.TagPolicyClause{
        {AllowedAny: []string{"safe", "read-only"}},
        {DeniedAny: []string{"destructive"}},
    }),
)
```

Tag filtering is applied twice with the same predicate:

- before planner prompting via `PlannerContext.AdvertisedToolDefinitions()`
- before tool execution as an invariant check

### Runtime Policy Override

Override registered agent policy in-process:

```go
err := rt.OverridePolicy(agent.Ident("service.chat"), runtime.RunPolicy{
    MaxToolCalls:                  10,
    MaxConsecutiveFailedToolCalls: 2,
    TimeBudget:                    5 * time.Minute,
    InterruptsAllowed:             true,
})
```

---

## Memory and Stores

### Memory Store

Persists run transcripts for planner context and observability:

```go
type Store interface {
    LoadRun(ctx context.Context, agentID, runID string) (Snapshot, error)
    AppendEvents(ctx context.Context, agentID, runID string, events ...Event) error
}
```

**Event types:** `user_message`, `assistant_message`, `tool_call`, `tool_result`,
`planner_note`, `thinking`.

The runtime automatically subscribes to hooks and persists events when a memory
store is configured.

### Run event store (runlog.Store)

The runtime also maintains a canonical, append-only run event log used for
introspection, audit/debug UIs, and deriving compact `run.Snapshot` values.

```go
type Store interface {
    Append(ctx context.Context, e *runlog.Event) error
    List(ctx context.Context, runID string, cursor string, limit int) (runlog.Page, error)
}
```

The runtime exposes:

- `Runtime.ListRunEvents(ctx, runID, cursor, limit)` for cursor-paginated listing
- `Runtime.GetRunSnapshot(ctx, runID)` for a compact snapshot derived from replaying the run log

Configure the store via `runtime.WithRunEventStore(...)`. If not set, the runtime
defaults to an in-memory implementation (`runtime/agent/runlog/inmem`).

The run log is also the canonical hydration source for planner resumes:
`ToolCallScheduledEvent` stores the authoritative tool payload, and
`ToolResultReceivedEvent` stores the authoritative result JSON plus
planner-visible outcome metadata and server-only sidecars once. Planner
activity inputs now carry tool-call references only and reload canonical state
on demand instead of accumulating duplicated summaries in workflow history.

### Run Phases

Finer-grained lifecycle tracking for UIs:

```go
const (
    PhasePrompted       = "prompted"        // Input received
    PhasePlanning       = "planning"        // Planner deciding
    PhaseExecutingTools = "executing_tools" // Tools running
    PhaseSynthesizing   = "synthesizing"    // Final response
    PhaseCompleted      = "completed"
    PhaseFailed         = "failed"
    PhaseCanceled       = "canceled"
)
```

---

## History Policies

Control how conversation history is managed before each planner turn:

### KeepRecentTurns

Sliding window that preserves system messages and recent turns:

```go
// DSL
RunPolicy(func() {
    History(func() {
        KeepRecentTurns(20)
    })
})
```

### Compress

Model-assisted summarization for long conversations:

```go
// DSL
RunPolicy(func() {
    History(func() {
        Compress(30, 10) // Trigger at 30 turns, keep 10 recent
    })
})

// Registration
cfg := chat.ChatAgentConfig{
    Planner:      myPlanner,
    HistoryModel: smallModelClient, // For compression
}
```

---

## Prompt Caching

Configure automatic cache checkpoint placement:

```go
// DSL
RunPolicy(func() {
    Cache(func() {
        AfterSystem()  // Checkpoint after system messages
        AfterTools()   // Checkpoint after tool definitions
    })
})
```

The runtime populates `model.Request.Cache` when planners don't set it explicitly.
Provider behavior is adapter-specific: Bedrock maps these checkpoints onto native
cache primitives, while the OpenAI Responses adapter currently rejects
cache-bearing requests explicitly because it cannot preserve the checkpoint
contract.

---

## System Reminders

Deliver structured, rate-limited guidance to models:

```go
input.Agent.AddReminder(reminder.Reminder{
    ID:              "pending_todos",
    Text:            "Review pending todo items before proceeding.",
    Priority:        reminder.TierGuidance,
    Attachment:      reminder.Attachment{Kind: reminder.AttachmentUserTurn},
    MaxPerRun:       3,
    MinTurnsBetween: 2,
})

// Remove when no longer relevant
input.Agent.RemoveReminder("pending_todos")
```

**Tiers:**

| Tier | Purpose |
|------|---------|
| `TierSafety` | Never suppressed (P0) |
| `TierGuidance` | Soft nudges, first to suppress (P2) |

---

## Model Clients

### Registration

```go
// Register model client
err := rt.RegisterModel("bedrock", bedrockClient)

// Create Bedrock client via runtime helper
client, err := rt.NewBedrockModelClient(awsClient, runtime.BedrockConfig{
    DefaultModel:   "us.anthropic.claude-3-5-sonnet-20240620-v1:0",
    HighModel:      "us.anthropic.claude-3-opus-20240229-v1:0",
    SmallModel:     "us.anthropic.claude-3-haiku-20240307-v1:0",
    MaxTokens:      4096,
    ThinkingBudget: 10000,
})

// Create OpenAI client via runtime helper
openAIClient, err := rt.NewOpenAIModelClient(runtime.OpenAIConfig{
    APIKey:         os.Getenv("OPENAI_API_KEY"),
    DefaultModel:   "gpt-5-mini",
    HighModel:      "gpt-5",
    SmallModel:     "gpt-5-nano",
    MaxTokens:      4096,
    ThinkingEffort: "high",
})
```

Runtime-owned model factories are transcript-stateless. Callers must pass the
complete provider-ready transcript in `model.Request.Messages`, and the runtime
persists canonical transcript deltas so they can be replayed from the durable
runlog when needed.

When planners render prompts through `RenderPrompt`, copy prompt provenance into model requests:

```go
content, err := input.Agent.RenderPrompt(ctx, "aura.chat.system", map[string]any{
    "AssistantName": "Ops Assistant",
})
if err != nil {
    return nil, err
}

resp, err := modelClient.Complete(ctx, &model.Request{
    Messages:   input.Messages,
    PromptRefs: []prompt.PromptRef{content.Ref},
})
```

### Rate Limiting

Apply adaptive rate limiting:

```go
import mdlmw "goa.design/goa-ai/features/model/middleware"

rl := mdlmw.NewAdaptiveRateLimiter(
    ctx,
    throughputMap,     // *rmap.Map for cluster-wide state (nil for local)
    "bedrock:sonnet",  // Model family key
    80_000,            // Initial TPM
    1_000_000,         // Max TPM
)

limitedClient := rl.Middleware()(rawClient)
rt.RegisterModel("bedrock", limitedClient)
```

---

## Run Options

Customize run behavior with functional options:

```go
client.Run(ctx, "session-1", msgs,
    runtime.WithRunID("custom-run-id"),
    runtime.WithTurnID("turn-1"),
    runtime.WithLabels(map[string]string{"tenant": "acme"}),
    runtime.WithMetadata(map[string]any{"request_id": "abc"}),
    runtime.WithTaskQueue("custom-queue"),
    runtime.WithMemo(map[string]any{"workflow_name": "Chat"}),
    runtime.WithSearchAttributes(map[string]any{"tenant": "acme"}),
    runtime.WithTiming(runtime.Timing{
        Budget: 2 * time.Minute,
        Plan:   30 * time.Second,
        Tools:  60 * time.Second,
    }),
)
```

Search attributes are passed through to the workflow engine as caller-owned
index metadata. The runtime does not mirror `SessionID` into engine search
attributes automatically.

`Timing.Plan` and `Timing.Tools` are semantic attempt budgets. They bound how
long a healthy planner or tool attempt may run once execution starts. Queue-wait
timeouts and heartbeat-based liveness detection are engine-specific concerns and
belong in the engine adapter, not the generic runtime API.

---

## Introspection

Query registered agents and tools:

```go
// List registered agents
agents := rt.ListAgents()  // []agent.Ident

// List registered toolsets
toolsets := rt.ListToolsets()  // []string

// Get tool spec
spec, ok := rt.ToolSpec(tools.Ident("helpers.search"))

// Get parsed tool schema
schema, ok := rt.ToolSchema(tools.Ident("helpers.search"))

// Get specs for an agent
specs := rt.ToolSpecsForAgent(agent.Ident("service.chat"))
```

---

## Engine Integration

### Engine Interface

```go
type Engine interface {
    RegisterWorkflow(ctx, def WorkflowDefinition) error
    RegisterRecordActivity(ctx, name, opts, fn) error
    RegisterPlannerActivity(ctx, name, opts, fn) error
    RegisterExecuteToolActivity(ctx, name, opts, fn) error
    StartWorkflow(ctx, req WorkflowStartRequest) (WorkflowHandle, error)
    QueryRunStatus(ctx, runID string) (RunStatus, error)
}
```

### WorkflowContext

Workflow handlers receive a context for deterministic operations:

```go
type WorkflowContext interface {
    Context() context.Context
    WorkflowID() string
    RunID() string
    Now() time.Time  // Deterministic time
    NextSequence() uint64
    PublishRecord(ctx, call) error
    ExecutePlannerActivity(ctx, call) (*api.PlanActivityOutput, error)
    ExecuteToolActivity(ctx, call) (*api.ToolOutput, error)
    ExecuteToolActivityAsync(ctx, call) (Future[*api.ToolOutput], error)
    PauseRequests() Receiver[api.PauseRequest]
    ResumeRequests() Receiver[api.ResumeRequest]
    ClarificationAnswers() Receiver[api.ClarificationAnswer]
    ExternalToolResults() Receiver[api.ToolResultsSet]
    ConfirmationDecisions() Receiver[api.ConfirmationDecision]
    StartChildWorkflow(ctx, req) (ChildWorkflowHandle, error)
    SetQueryHandler(name, handler) error
}
```

### Available Engines

**Temporal worker** — Production-grade durable execution:

```go
import temporal "goa.design/goa-ai/runtime/agent/engine/temporal"

eng, _ := temporal.NewWorker(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:  "temporal:7233",
        Namespace: "default",
    },
    WorkerOptions: temporal.WorkerOptions{
        TaskQueue: "orchestrator.chat",
    },
    ActivityDefaults: temporal.ActivityDefaults{
        Planner: temporal.ActivityTimeoutDefaults{
            QueueWaitTimeout: 30 * time.Second,
            LivenessTimeout:  20 * time.Second,
        },
        Tool: temporal.ActivityTimeoutDefaults{
            QueueWaitTimeout: 2 * time.Minute,
            LivenessTimeout:  20 * time.Second,
        },
    },
})
```

**Temporal client** — Start/query/signal without local polling:

```go
eng, _ := temporal.NewClient(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:  "temporal:7233",
        Namespace: "default",
    },
})
```

In this split:

- `RunPolicy.Timing.Plan` / `runtime.WithTiming(...).Plan` set the planner
  attempt budget.
- `RunPolicy.Timing.Tools` / `runtime.WithTiming(...).Tools` set the tool
  attempt budget.
- `temporal.Options.ActivityDefaults` sets Temporal-only queue-wait and
  heartbeat liveness behavior.

**In-memory** — Fast iteration, no durability:

```go
import inmem "goa.design/goa-ai/runtime/agent/engine/inmem"

eng := inmem.New()
```

---

## Telemetry

### Logger Interface

```go
type Logger interface {
    Debug(ctx context.Context, msg string, keyvals ...any)
    Info(ctx context.Context, msg string, keyvals ...any)
    Warn(ctx context.Context, msg string, keyvals ...any)
    Error(ctx context.Context, msg string, keyvals ...any)
}
```

### Metrics Interface

```go
type Metrics interface {
    IncCounter(name string, value float64, tags ...string)
    RecordTimer(name string, duration time.Duration, tags ...string)
    RecordGauge(name string, value float64, tags ...string)
}
```

### Tracer Interface

```go
type Tracer interface {
    Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Span)
    Span(ctx context.Context) Span
}
```

---

## Feature Modules

| Package | Purpose |
|---------|---------|
| `features/memory/mongo` | MongoDB-backed memory store |
| `features/prompt/mongo` | MongoDB-backed prompt override store |
| `features/runlog/mongo` | MongoDB-backed run event log store |
| `features/session/mongo` | MongoDB-backed session store |
| `features/stream/pulse` | Pulse message bus sink |
| `features/model/bedrock` | AWS Bedrock model client |
| `features/model/openai` | OpenAI-compatible model client |
| `features/model/anthropic` | Direct Anthropic Claude API client |
| `features/model/gateway` | Remote model gateway client |
| `features/model/middleware` | Rate limiting, logging, metrics |
| `features/policy/basic` | Basic policy engine |

---

## MCP Callers

The `runtime/mcp` package provides three caller implementations for different MCP server
transports.

### StdioCaller

Spawns an MCP server as a subprocess and communicates via stdin/stdout:

```go
import "goa.design/goa-ai/runtime/mcp"

caller, err := mcp.NewStdioCaller(mcp.StdioOptions{
    Command: "npx",
    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
    Env:     []string{"HOME=" + os.Getenv("HOME")},
})
if err != nil {
    log.Fatal(err)
}
defer caller.Close()
```

### HTTPCaller

HTTP POST to MCP endpoints:

```go
caller := mcp.NewHTTPCaller("https://mcp-server.example.com/mcp")
```

### SSECaller

Server-Sent Events for streaming MCP responses:

```go
caller := mcp.NewSSECaller(mcp.SSEOptions{
    URL: "https://mcp-server.example.com/sse",
})
```

All callers implement the `mcp.Caller` interface and include automatic retry via
`runtime/mcp/retry`.

### Server-initiated events (Broadcaster)

Generated MCP adapters can stream server-initiated events (notifications, resource updates) to multiple
subscribers via `mcp.Broadcaster`. The default in-memory implementation is:

```go
b := mcp.NewChannelBroadcaster(128, true) // (buf, drop)
sub, _ := b.Subscribe(ctx)
defer sub.Close()
```

### Repair prompts for invalid params (retry.RetryableError)

When an MCP server reports invalid parameters and a structured repair prompt is available, generated
clients may return `retry.RetryableError` with a deterministic `Prompt`. This is intended for LLM-driven
correction: the model returns JSON-only corrected params, which are decoded into the operation payload and retried.

---

## Stream Profiles

Stream profiles control which events reach different audiences. Use profiles to filter
events for specific use cases.

| Profile | Purpose | Events Included |
|---------|---------|-----------------|
| `DefaultProfile()` | All events, child runs linked | All event types |
| `UserChatProfile()` | End-user chat UIs | Same as default |
| `AgentDebugProfile()` | Debug view | All event types |
| `MetricsProfile()` | Telemetry and monitoring | `usage`, `workflow` only |

```go
import "goa.design/goa-ai/runtime/agent/stream"

// Get a profile
profile := stream.AgentDebugProfile()

// Profiles are used internally by stream subscribers
// to filter events before delivery
```

---

## Tool Errors

The `runtime/agent/toolerrors` package provides structured error types for tool execution
failures that integrate with the planner retry system.

```go
import "goa.design/goa-ai/runtime/agent/toolerrors"

// Create a tool error with retry hint
err := toolerrors.New(
    toolerrors.WithMessage("Database connection failed"),
    toolerrors.WithRetryable(true),
    toolerrors.WithHint("Check database connectivity and retry"),
)

// Check if error is retryable
if toolerrors.IsRetryable(err) {
    // Handle retry logic
}

// Tool errors are automatically converted to planner.RetryHint
// for planners to handle gracefully
```

### Validation Issues and Retry Hints

Tool calls can fail because the input payload is missing fields, violates constraints,
or has the wrong JSON shape. When that happens, callers generally need actionable,
field-level feedback rather than a generic failure string.

Goa‑AI supports two complementary paths that produce `planner.RetryHint`:

1. **Decode‑time validation (generated codecs)**  
   The generated tool codec validates the tool JSON payload before execution.
   If validation fails, the codec returns a generated validation error that exposes
   structured issues (`Issues() []*tools.FieldIssue`) and descriptions. The runtime
   converts these into `planner.RetryHint` automatically (missing fields, enum values,
   etc.).

2. **Execution‑time validation (service / tool provider errors)**  
   When a tool provider calls a bound service method, the method may return a Goa
   validation error (for example `goa.MissingFieldError`, `goa.InvalidLengthError`, …).
   Providers should surface these as **structured validation issues** in the tool result
   message so consumers can build a `RetryHint` without parsing error strings.

   - **Provider behavior (generated)**: generated providers call
     `toolregistry.ValidationIssues(err)` and, when issues are present, emit an error
     result that includes them.
   - **Wire protocol**: tool result errors may include `issues` (`[]FieldIssue`).
   - **Consumer behavior**: registry executors convert `issues` into a `RetryHint`
     (e.g., `missing_fields`) and attach the tool spec example input when available.

This keeps the contract strong and deterministic: validation stays at boundaries,
and “what to retry with” is computed from structured data, not heuristics.

---

## Model Middleware

The `features/model/middleware` package provides middleware for model clients.

### Adaptive Rate Limiter

Apply adaptive rate limiting to handle provider throttling:

```go
import mdlmw "goa.design/goa-ai/features/model/middleware"

rl := mdlmw.NewAdaptiveRateLimiter(
    ctx,
    throughputMap,     // *rmap.Map for cluster-wide state (nil for local)
    "bedrock:sonnet",  // Model family key
    80_000,            // Initial TPM (tokens per minute)
    1_000_000,         // Max TPM
)

limitedClient := rl.Middleware()(rawClient)
rt.RegisterModel("bedrock", limitedClient)
```

The rate limiter automatically adjusts throughput based on provider responses and
handles 429 (rate limited) errors with exponential backoff.

---

## Common Patterns

### Bootstrap Helper

Generated `goa example` emits `cmd/<service>/agents_bootstrap.go`:

```go
// Bootstrap creates runtime with Temporal, stores, and registers agents
rt, cleanup, err := bootstrap.New(ctx)
if err != nil {
    log.Fatal(err)
}
defer cleanup()
```

### Pulse Streaming

```go
import pulsestream "goa.design/goa-ai/features/stream/pulse"

streams, _ := pulsestream.NewRuntimeStreams(pulsestream.RuntimeStreamsOptions{
    Client: pulseClient,
})

rt := runtime.New(
    runtime.WithEngine(eng),
    runtime.WithStream(streams.Sink()),
)

// Subscribe to session events
sub, _ := streams.NewSubscriber(pulsestream.SubscriberOptions{SinkName: "ui"})
events, errs, cancel, _ := sub.Subscribe(ctx, "session/session-123")
defer cancel()

// Consume until you observe `type=="run_stream_end"` for the active run ID.
```

### Custom Tool Executor

```go
executor := runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error) {
    // Access explicit metadata
    log.Printf("Executing %s in run %s, session %s", call.Name, meta.RunID, meta.SessionID)
    
    // Call your service
    result, err := myService.Execute(ctx, call.Payload)
    if err != nil {
        return nil, err
    }
    
    return &planner.ToolResult{
        Name:   call.Name,
        Result: result,
    }, nil
})
```

---

## Error Handling

### Sentinel Errors

```go
var (
    ErrAgentNotFound       = errors.New("agent not found")
    ErrEngineNotConfigured = errors.New("runtime engine not configured")
    ErrInvalidConfig       = errors.New("invalid configuration")
    ErrMissingSessionID    = errors.New("session id is required")
    ErrWorkflowStartFailed = errors.New("workflow start failed")
    ErrRegistrationClosed  = errors.New("registration closed after first run")
)
```

### Run Store Errors

```go
var ErrNotFound = errors.New("run not found")  // run.ErrNotFound
```

### Model Errors

```go
var ErrStreamingUnsupported = errors.New("model: streaming not supported")
var ErrRateLimited = errors.New("model: rate limited")
```

---

## Best Practices

1. **Register before running.** All agents and models must be registered before
   the first `Run` or `Start` call. Registration closes afterward.

2. **Use generated clients.** The typed `<agent>.NewClient(rt)` embeds route
   information and provides compile-time safety.

3. **Choose one streaming path.** Use `PlannerModelClient` for runtime-owned
   event emission, or use raw `ModelClient` with `planner.ConsumeStream` (or
   manual draining) when you want explicit control.

4. **Set SessionID for sessionful runs.** `Run` and `Start` require a session ID
   for grouping and memory association. `OneShotRun` is explicitly sessionless.

5. **Trust the contracts.** Don't add defensive checks for values guaranteed by
   Goa validation or construction. Let violations fail fast.

6. **Configure stores for production.** In-memory defaults are suitable for
   development; use MongoDB stores for persistence.

7. **Stream events, don't poll.** Use `SubscribeRun` or Pulse subscriptions
   instead of polling run status.

8. **Keep planners focused.** Planners decide what to do (final answer vs. tools).
   Tool implementations handle how.

---

## Glossary

| Term | Definition |
|------|------------|
| **Run** | A single workflow execution. Has a unique RunID. |
| **Session** | Groups related runs (e.g., multi-turn conversation). |
| **Turn** | A user message → agent response cycle. May span multiple runs if interrupted. |
| **Planner** | Decision-maker that analyzes messages and returns tool calls or final responses. |
| **Toolset** | Collection of related tools with shared execution logic. |
| **Tool Spec** | Metadata and JSON codecs for a tool (name, schema, codec functions). |
| **Bounds** | Metadata describing how a tool result was truncated or limited. |
| **Hook** | Internal event emitted for observability (memory, streaming, telemetry). |
| **Stream Event** | Client-facing event delivered via Sink (tool progress, assistant replies). |
| **Finalizer** | Aggregates child results into parent tool result for agent-as-tool (does not propagate artifacts). |
| **Reminder** | Structured backstage guidance injected into planner prompts. |
