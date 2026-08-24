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
| **Engine** | Workflow backend (Temporal or in-memory). Provides durable execution, activities, terminal-result queries, and cancellation. |
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
    temporalEng, err := temporal.NewWorker(temporal.Options{
        ClientOptions: &client.Options{HostPort: "temporal:7233"},
        WorkerOptions: temporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
    })
    if err != nil {
        log.Fatal(err)
    }
    defer func() {
        if err := temporalEng.Close(); err != nil {
            log.Printf("close Temporal engine: %v", err)
        }
    }()

    // MongoDB stores for persistence.
    // The low-level client is a *mongo.Client from go.mongodb.org/mongo-driver/v2/mongo.
    mongoClient := newMongoClient()
    memClient, _ := memorymongoclient.New(memorymongoclient.Options{
        Client:   mongoClient,
        Database: "agents",
    })
    memStore, _ := memorymongo.NewStore(memClient)

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

- typed result and union types
- private specs containing each result schema and generated codec
- a narrow `<Name>Example()` accessor that returns an immutable copy of
  canonical JSON when the result has an authored root `Example(...)`
- public `Complete<Name>(ctx, client, req)` wrappers that own unary decoding
- public typed `StreamComplete<Name>(ctx, client, req)` wrappers that own
  streaming validation and decoding

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

Every low-level `model.StructuredOutput` must also provide a nonempty `Name`.
The shared request boundary rejects a missing name before any provider call;
generated completion helpers derive the name from the validated completion DSL.

Unary completion makes exactly one model call. The response is decoded with the
generated codec. If its JSON violates the completion contract, the helper
returns a non-retryable `planner.OutputContractError` and does not ask the model
again. Provider errors and malformed response envelopes are also returned
immediately. On success, `completion.Response.ModelResponse` contains the exact
model response and its token usage. On failure, the response is nil, matching
the `model.Client` contract.

When upgrading existing completion callers:

- replace `completion.Complete(ctx, client, req, completions.Spec<Name>)` with
  `completions.Complete<Name>(ctx, client, req)`;
- replace `completion.Stream(ctx, client, req, completions.Spec<Name>)` with
  `completions.StreamComplete<Name>(ctx, client, req)`;
- keep reading the unary typed value from `Response.Value`, and replace
  `Response.Attempts[0]` with `Response.ModelResponse`;
- replace generated `Decode<Name>` and `Decode<Name>Chunk` calls with the
  generated unary or streaming wrapper; and
- use `<Name>Example()` only when application code needs the authored example.

The generated codec and schema no longer have a public accessor. Invalid output
returns a nil unary response because no accepted typed completion exists. The
wire format is unchanged.

Only root examples explicitly authored with Goa `Example(...)` are shown to
models. Codegen emits the annotated schema, the schema without its root example,
and standalone canonical example JSON. Adapters use the provider-native
structured-output example field when one exists; they never promote synthesized
Goa examples.

Streaming completions return a typed `completion.Streamer[T]`. Preview chunks
remain available through `Recv`, while `Value` stays unavailable until the
provider stream ends and its final response matches the final completion chunk:

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
    // Use preview completion_delta chunks here when useful.
    _ = chunk
}
value, ok := stream.Value()
if !ok {
    panic("completion stream ended without a typed value")
}
fmt.Println(value.Name)
```

Typed completion helpers are intentionally strict:

- Unary helpers accept unary requests only.
- Completion names are validated at the DSL boundary: 1-64 ASCII characters,
  letters/digits/`_`/`-` only, and must start with a letter or digit.
- Unary and streaming helpers reject tool-enabled requests and caller-supplied `StructuredOutput`.
- Unary helpers make one provider request. A generated-codec rejection returns a
  non-retryable `planner.OutputContractError` without another model request.
- Streaming providers may emit `completion_delta*` preview fragments and emit exactly one final `completion` chunk, or reject the request explicitly.
- Streaming helpers hold the final value until the stream ends normally and
  the provider's complete response contains the same value. They never restart
  after exposing previews; an invalid stream returns the same non-retryable
  output-contract error without exposing the final value.
- `Value` becomes available only after `Recv` reaches and validates the final
  completion; there is no separate decoder that can accept an unchecked chunk.
- Completion streams use their generated typed wrapper directly; do not route
  them through planner streaming helpers, which are for assistant transcript
  text and tool execution events.
- Providers that do not implement structured output surface `model.ErrStructuredOutputUnsupported`.
- Generated schemas are canonical and provider-neutral; provider adapters may normalize them to a supported subset, but must fail explicitly when they cannot preserve the declared contract.

### Model request and output bounds

Every model request is checked before the client copies it or calls observers
and providers. Messages, media, prompt references, tool names and schemas, and
structured-output contracts share one limit of 16 MiB and 100,000 visited
values. Collections are checked before the client allocates their copies.

Every unary model response is checked before the runtime copies, fingerprints,
or decodes it. Model-controlled strings, raw JSON, binary data, citation source
content, metadata keys and values, and tool-result content share one limit of
16 MiB and 100,000 visited values. Each nested metadata or tool-result value may
be at most 64 levels deep. Collections are checked before the runtime allocates
their copies.

A stream applies the same limits to the cumulative chunks and to its complete
response. The complete response is also bounded independently. Before copying
it, reconciliation exempts only text, citations, reasoning, tool calls, usage,
and stop data that exactly repeat accepted chunks. Final-response wrappers,
metadata, and any new or mismatched data consume the remaining shared budget.
Internal fingerprints, ownership copies, observer copies, and planner text
accumulation do not create additional budget charges. A stream that exceeds
either limit fails before growing chunk accumulators or copying its complete
response.

`TokenUsage.Model` records only a concrete model identity reported by the
provider. It remains empty when the provider omits that identity; the client
does not substitute `Request.Model`. `TokenUsage.ModelClass` records the logical
class from the immutable request contract.

Canonical metadata and tool-result values may contain nil, booleans, finite
numbers, valid UTF-8 strings, byte slices, arrays, slices, and maps with valid
UTF-8 string keys. Structs and pointers are not canonical values. A bounded
struct may be retained only inside rejected-response evidence so an observer can
diagnose the contract failure; pointers are unsupported even there because
their target cannot be copied safely. Reference cycles, invalid UTF-8, and
unsupported kinds fail explicitly. The runtime never truncates, coerces,
repairs, or silently omits model output.

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
| Assistant `tool_use` + user `tool_result` transcript replay | Supported for OpenAI-representable assistant turns; version 2 function-call metadata retains its canonical agreement payload, so replay validation remains independent of the currently advertised tool catalog. Unversioned persisted metadata keeps the original exact-arguments agreement contract. Unknown versions and malformed combinations fail closed; tool-result errors stay explicit |
| Streaming text | Supported |
| Streaming `tool_call_delta` and final `tool_call` | Supported |
| Streaming usage and stop chunks | Supported |
| Model-class routing (`default`, `high-reasoning`, `small`) | Supported |
| Structured output (`completion_delta` + final `completion`) | Supported via OpenAI `json_schema` response format, but not in combination with tools |
| Strict schemas | Tool and structured-output schemas are always sent with `strict:true`; the adapter projects canonical schemas onto the strict subset (closed objects, all members required, optionals nullable) and canonicalizes returned payloads by dropping the null members the projection introduced. Root unions, unsupported composition and validation keywords, open objects or schema-valued `additionalProperties`, more than 5,000 properties, more than 1,000 enum values, more than 120,000 characters across property names, definition names, enum strings, and string constants, or nesting beyond 10 levels are rejected before the provider call. An enum with more than 250 string values may contain at most 15,000 characters. Fine-tuned model IDs beginning with `ft:` additionally reject unsupported string, numeric, array, and `patternProperties` constraints |
| Cache options / cache checkpoints | Rejected explicitly |
| Thinking | Only the representable subset is supported: `Thinking.Enable` maps to configured OpenAI `reasoning_effort`; budgeted or interleaved thinking requests fail fast. A request that explicitly combines thinking with temperature also fails; a configured default temperature is omitted from thinking requests |

This is the intended migration seam for Aura-style inference backends: swap the
provider adapter, keep planners and runtime flow unchanged.

Model adapters are stateless at the transcript boundary. They never rehydrate
history from a `RunID`; runtime-owned callers must supply the full transcript,
and durable recovery rebuilds that transcript from runlog
`transcript_messages_appended` records.

### Canonical message metadata and citations

`model.Message.Meta` carries provider-authored replay data. Boundaries that
persist or transport that map should use `model.MarshalMetadata` and
`model.UnmarshalMetadata`: metadata is one JSON object, decoded numbers remain
`json.Number`, trailing data and non-object roots are rejected, and nil or an
empty object canonicalizes to nil. Before a response reaches planner code,
copying and fingerprinting reject metadata with reference cycles, more than 64
nested values, or more than 100,000 visited values in one metadata object or
tool-result value. Strings, byte slices, and map keys in one such value may
total at most 16 MiB. Accepted metadata and tool-result values contain nil,
booleans, finite numbers, strings, string-keyed maps, slices, and arrays.
Structs and pointer-shaped values are retained only as bounded rejection
evidence and are not accepted as canonical model data.

Citation replay remains provider-specific because a canonical citation must not
be flattened into ordinary text. Bedrock reconstructs assistant
`CitationsPart` values as native citation content blocks for document character,
chunk, and page locations. Bedrock system citations remain unsupported because
the SDK's system-content union has no citation member. The Anthropic and Vertex
adapters continue to reject canonical citation replay when they cannot
reconstruct every provider-required field from the canonical part.

### Sampling parameters on current-generation Claude models

Anthropic removed the `temperature`/`top_p`/`top_k` sampling parameters from
current-generation Claude models (Opus 4.7 and later, Sonnet 5 and later,
Haiku 5 and later, and the Fable/Mythos generation): a request carrying a non-default value is
rejected with a 400 `invalid_request_error` ("temperature is deprecated for
this model"). The Anthropic adapter (`features/model/anthropic`, which also
backs Claude-on-Vertex) and the Bedrock adapter (`features/model/bedrock`)
share one capability rule and omit `temperature` from the wire request for
those models instead of forwarding a guaranteed failure — the model runs at
its own default sampling behavior, and a configured `Options.Temperature` or
`Request.Temperature` has no effect. The Anthropic adapter records the
omission on the ambient trace span (`gen_ai.request.temperature_omitted`).
Steer output behavior through prompting on these models; older generations
(Opus ≤ 4.6, Sonnet 4.x, and Haiku ≤ 4.5) keep honoring the configured value
unchanged.

### Thinking and tool choice on Claude

Claude models with adaptive thinking accept tools and forced tool choice.
Models with older manual thinking accept tools selected with `auto` or `none`,
but reject forced choices (`any` or one named tool) while thinking is enabled.
Mythos Preview is the exception among adaptive models: it also rejects forced
tool choice. The Anthropic and Bedrock adapters resolve the model and the
effective tool choice, including a private structured-output tool when needed,
before sending the request. Unsupported combinations fail locally.

The direct Anthropic Messages adapter uses native structured output on Claude
Sonnet, Opus, and Haiku 4.5 or later. Bedrock Converse uses native
`OutputConfig` only for the Claude 4.5 and 4.6 models Bedrock documents as
supporting it. Other Claude models on Bedrock use one private forced tool and
the framework validates its result against the same completion contract.

### Thinking on Gemini 3

Gemini 3 uses thinking levels rather than numeric thinking-token budgets, and
thinking cannot be disabled. The provider-neutral request can enable the
model's default thinking behavior, but a numeric budget or explicit
`Thinking.Enable=false` is rejected. API-valid configured and per-request
temperatures are forwarded to Vertex unchanged. A client-level numeric
`ThinkingBudget` applies only to older Gemini models that accept token budgets;
Gemini 3 does not inherit it.

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
5. **Repeat** — Loop continues until planner returns a `FinalResponse`,
   `FinalToolResult`, or a successful `TerminalRun` tool completes the run

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

When the planner receives a reply but rejects it because the reply does not
follow the required rules, return
`planner.NewOutputContractError(violation)`. Temporal will not ask for the same
reply again. Do not use `OutputContractError` for model-provider failures,
timeouts, canceled work, or network errors; those keep their existing retry
behavior.

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
    SynthesisOnly bool                 // Final response required; tools forbidden
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

Bookkeeping exception:

- tools declared with `Bookkeeping()` still execute and still publish durable
  run events,
- every call and result remains in the model-visible provider transcript so
  signed assistant responses are replayed unchanged,
- successful results are omitted from compact
  `PlanResumeInput.ToolOutputs` and do not force another planner turn,
- except when a bookkeeping tool fails with a `ToolFailure` whose
  `AllowsToolTurn()` method returns true: that recoverable failure enters
  `ToolOutputs` so the next resume turn can repair and resend the tool call,
- bookkeeping tools may still request a clarification in the same turn through
  `ToolExecutionResult.Clarification`; that external-input request remains legal
  even when no successful bookkeeping result requires a resume.

Workflow step boundary:

- the runtime treats each admitted `PlanResult` as one workflow step,
- immediate tool execution, confirmations, and await-provided results append to
  one step batch and use the same recorder for durable events and
  planner-visible transcript/tool-output state,
- after the batch is complete, one transition policy decides resume, finish,
  terminal-tool finish, or forced finalization,
- terminal planner payloads are exclusive except for hidden, non-terminal
  bookkeeping side effects that complete successfully in the same step,
- a run may supply `LimitTerminalPlans`, one payload-only `TerminalRun()` call
  for each of the time, tool-call, and consecutive failed-call limits; before
  the first planner activity, the runtime validates the complete set against
  the agent's registered generated codecs and rejects tools that require
  confirmation,
- when one of those limits is reached, the workflow selects the matching call
  without loading saved messages, adds current run identifiers and labels,
  and executes the call through the existing terminal-tool path,
- when the run omitted `LimitTerminalPlans`, or a tool failure requires
  finalization, `PlanResumeInput.Finalize` is non-nil and the planner may close
  through terminal bookkeeping tools instead of prose; the runtime admits only
  `TerminalRun()` calls (`TerminalRun()` implies bookkeeping), executes them
  inside the remaining hard-deadline window, stamps generated tool-call IDs
  with an opaque SHA-256 digest of length-delimited run ID, turn ID, attempt,
  batch index, and exact tool name, and requires every terminal side effect in
  the batch to complete successfully,
- before executing either a fixed limit call or a terminal call returned by the
  finalization planner, the runtime writes the exact
  `planner.TerminationReason` to
  `runtime.FinalizationReasonLabel` (`goa-ai.finalization_reason`); run labels,
  policy labels, planner output, and model output cannot replace this value,
- ordinary tool calls do not receive the finalization-reason label; the runtime
  removes that reserved key if a run, policy, planner, or model supplies it,
- recoverable failures supply one normal planner activity with their structured
  evidence and do not constrain this validated terminal bookkeeping path;
  caller-supplied `WithRestrictToTool` remains run-scoped and still applies,
- deadline checks happen before admitting new work; in-flight tool batches
  still respect the finalizer window and synthesize canceled tool results for
  unfinished calls.

Migration: `runtime.LimitReasonLabel` and `goa-ai.limit_reason` were removed.
Consumers of fixed-limit and planner-authored finalization calls, including
`tool_failure`, must read `runtime.FinalizationReasonLabel` at
`goa-ai.finalization_reason`.

`LimitTerminalPlans` and `CompletionTool` add fields to the Temporal workflow
input. Deploy the runtime, generated workers, and callers as one coordinated
cutover. Mixed versions are unsupported. New suspensions use
`goa-ai.run-suspension.v3`; workers accept only that exact checkpoint version,
so work and suspensions created by the previous release may fail after the
cutover.

Run-scoped completion tool:

- `WithRunCompletionTool(tool_name)` makes one successful execution of the
  named budgeted tool the run's complete result; the runtime ends immediately
  without asking the planner for a final assistant response,
- the completion attempt must be the only action in its planner response; it
  cannot accompany another call or an await request, and no call in a
  completion-policy run may request post-tool synthesis because the required
  next terminal answer cannot complete that run,
- the planner cannot delegate the completion tool through external await work,
  and denying its execution at a confirmation prompt fails the run,
- correctable failures return to the planner while the normal tool and failure
  budgets permit another attempt,
- a planner-authored final response, a non-recoverable tool failure, exhausted
  caps, or an exhausted deadline fails the run when the completion tool has not
  succeeded,
- the named tool must belong to the executing agent, be non-bookkeeping,
  non-terminal, and be allowed by every other run tool policy,
- `CompletionTool` and `LimitTerminalPlans` cannot be set on the same run
  because they assign conflicting outcomes to exhausted limits. This run-scoped
  behavior does not change the tool's behavior in runs that omit the option.

### Planner step and failed-result contracts

```go
type PlanResult struct {
    ToolCalls     []ToolRequest    // Tools to execute; only bookkeeping may accompany a terminal payload
    SynthesizeAfterTools bool      // Synthesize after success; repair remains allowed
    FinalResponse *FinalResponse   // Terminal assistant message
    FinalToolResult *FinalToolResult // Terminal tool result for nested agent runs
    Streamed      bool             // True if text was already streamed via Events
    Await         *Await           // Request clarification or external tool results
    ExpectedChildren int           // Optional hint for nested child results
    Notes         []PlannerAnnotation
}

type ToolOutput struct {
    Name       tools.Ident
    ToolCallID string
    Payload    rawjson.Message
    Result     rawjson.Message
    Failure    *ToolFailure        // Mutually exclusive with Result
}
```

These fields answer different questions:

| Contract | Scope | Question answered |
| --- | --- | --- |
| `ToolSpec.Tags` | One tool for every run | Which flat labels are available to generic policy and UI filtering? |
| `ToolSpec.Meta` | One tool for every run | Which inert generated annotations are available to their named consumers? Metadata alone changes no runtime behavior. |
| `ToolSpec.Bookkeeping` | One tool for every run | Does this call consume retrieval or failure budget, and does its success independently schedule another planner turn? |
| `ToolSpec.TerminalRun` | One tool for every run | Does successful execution itself complete the run? |
| `ToolOutput.Failure.Recovery.Action` | One failed result | Must the planner correct this call, replan, or finish without tools? |
| `PlanResult.SynthesizeAfterTools` | One selected batch | If the batch has no recoverable failure, must the next turn answer? |
| `PlanResumeInput.SynthesisOnly` | One planner activity | Must this planner result be terminal and tool-free? |
| `PlanResumeInput.Finalize` | Runtime-forced planner termination | Did an unconfigured cap or deadline, or one tool failure, require the planner to finish? |

The runtime applies them in this order:

| Completed step | Next state |
| --- | --- |
| Configured `CompletionTool` executed successfully | End immediately |
| `CompletionTool` is configured and another terminal condition occurs | Fail because the required tool did not succeed |
| Cap or deadline exhausted with `LimitTerminalPlans` | Execute the matching terminal call |
| Cap or deadline exhausted without either completion policy | Forced `Finalize` turn |
| Successful `TerminalRun` tool without `CompletionTool` | End immediately |
| Any failure permits tools | Runtime-enforced correction or replan turn |
| `SynthesizeAfterTools` requested | `SynthesisOnly` turn |
| Otherwise | Normal continuation turn |

`SynthesizeAfterTools` therefore does not create a second recovery policy.
`ToolFailure.Recovery` is the single transition contract: `correct_call` keeps
the failed tool available and attaches generated correction evidence without
requiring a retry. The planner may combine work, call any advertised tool, await
input, or answer. Historical tool calls retain deterministic provider-name
projection independently of the current catalog. `replan` removes the failed
tool while permitting another advertised action, input request, or answer;
`finish` forbids more tools. When the same tool has both correction and replan
failures in one batch, the correctable failure keeps that tool available. A
recovery turn may end with an input suspension; continuing retains the selected failure
evidence. A failed batch clears its earlier
synthesis intent; a new retry batch may request `SynthesizeAfterTools` again.

The workflow selects current recovery outputs by stable call ID in
`PlanActivityInput`. Empty recovery IDs are omitted from canonical JSON.
Deploy workflow workers and generated callers as one coordinated hard cutover.
Only the current generated and persisted shapes are supported. Ongoing
workflows and saved suspensions may therefore fail after the cutover.

Bookkeeping turn invariant:

- if a planner turn emits any budgeted tool call, the runtime resumes as usual
  from the surviving planner-visible tool outputs,
- if a planner turn emits only successful bookkeeping tool calls, the same `PlanResult`
  must also resolve that turn without another reasoning resume: either a
  terminal outcome (`TerminalRun` tool or `FinalResponse` / `FinalToolResult`),
  or an external-input suspension,
- failed bookkeeping results are the planner-visible exception: the runtime
  resumes through the typed recovery transition; `correct_call` and `replan`
  may use tools, while `finish` resumes without tools for terminal synthesis,
- during forced finalization, terminal bookkeeping calls are not replayed into a
  later planner turn; they either durably close the run or fail finalization,
- otherwise the runtime fails fast instead of scheduling an implicit extra
  `PlanResume`.

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
    ModelClient(id string) (model.Client, bool)  // Opaque validated model client lookup
    PlannerModelClient(id string) (planner.PlannerModelClient, bool) // Planner-scoped client with selected-response event emission
    RenderPrompt(ctx context.Context, id prompt.Ident, data any) (*prompt.PromptContent, error)
    AddReminder(r reminder.Reminder)      // Register backstage guidance
    RemoveReminder(id string)             // Clear a reminder
}
```

Use `AdvertisedToolDefinitions()` when constructing provider requests inside planners. The
runtime filters registered tool specs before the planner/model sees them and strips tag metadata
from the model-facing `ToolDefinition` values. Provider adapters still encode
historical tool-use and tool-result blocks from the transcript independently of
the tools currently advertised.

Generated tool definitions also carry precomputed provider projections. The
DSL-authored top-level Goa `Example(...)` on a payload becomes the only
top-level provider example: providers that consume schema annotations use the
generated schema `example`, while Anthropic and Bedrock Claude adapters use the
generated schema without the root example plus top-level `input_examples`.
Runtime code does not parse or rewrite schemas to discover examples. Boundaries
that transport model tools between processes should use `model.ToolInputContract`
so the complete provider-neutral input contract stays intact until the provider
adapter chooses a projection.

### PlannerEvents

`PlannerEvents` lets planner code publish its own semantic progress. Model
response text, thinking, tool-argument previews, and usage are recorded and
published by the runtime; `planner.ConsumeStream` never emits events:

```go
type PlannerEvents interface {
    AssistantChunk(ctx context.Context, text string)
    ToolCallArgsDelta(ctx context.Context, toolCallID string, toolName tools.Ident, delta string)
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

`PlannerContext.PlannerModelClient(id)` returns a planner-scoped client that
records `AssistantChunk`, `PlannerThinkingBlock`, and tool-argument presentation
with its invocation. Its `Stream(...)` method drains the underlying provider
stream and returns a `planner.StreamSummary`. After `PlanResult` selects the
response, the runtime publishes that presentation and usage from every attempted
invocation. A planner client is single-use for the selected response; run probes
through `ModelClient` before obtaining it:

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
        FinalResponse: sum.FinalResponse(),
        Streamed: true, // Text was already streamed
    }, nil
}
```

This is the safest integration style because the planner-scoped client drains
the validated model stream and returns only the planner summary.

### Option 2: ConsumeStream with an Opaque Client

When you want direct control over the validated stream, fetch the opaque
`model.Client`
from `PlannerContext.ModelClient` and pair it with `planner.ConsumeStream`:

```go
mc, ok := input.Agent.ModelClient("bedrock")
if !ok {
    return nil, errors.New("model not configured")
}
st, err := mc.Stream(ctx, req)
if err != nil {
    return nil, err
}
sum, err := planner.ConsumeStream(ctx, st)
if err != nil {
    return nil, err
}
```

This helper only drains the stream and returns a `StreamSummary` with
accumulated text and tool calls. The runtime journal owns later presentation
and usage publication.

`ConsumeStream` accepts the `*model.ValidatedStream` returned by every public
model client. Provider and transport adapters capture a `model.RequestContract`
before inference and pass their internal `model.Streamer` to `ValidateStream`;
planner code never wraps or revalidates streams.

Each runtime-managed model call creates an isolated response candidate before
planner code receives the response or stream chunk. The candidate retains its
ordered text, thinking, and tool-argument deltas until the planner selects a
response. Every successful stream ends its typed chunks with clean EOF and
exposes exactly one canonical response through `ValidatedStream.Response()`. The
runtime captures and validates that response before returning EOF to planner
code, including when the provider is behind a model gateway. Incomplete
provider content blocks are contract errors. A validated stream is tied to the
model identity, structured-output contract, tool definitions, and generated
validators copied before provider work begins. Request mutation after that
point cannot change which output the stream accepts.

Tool definitions built from a generated `tools.ToolSpec` retain that tool's
generated payload decoder inside the process. Unary responses and final
streamed tool-call chunks must name a tool present in the request and pass that
decoder before planner code receives them. Caller-authored tools built with
`model.AdvertisedToolInputFromSchema` compile their JSON Schema once and apply
it at the same boundary. Requests reject tool definitions that carry only
schema bytes without either validation path.
When `PlanResult` contains tool calls, the runtime
matches their model-facing IDs, names, and payload bytes to exactly one
candidate and persists only that response's assistant transcript. Mixed,
copied, incomplete, or ambiguous provider outputs fail the planner activity.
Selecting a provider message as `FinalResponse` also requires the result to
preserve that response's complete tool-call set; a terminal result cannot
silently discard a provider-requested action.
Call order has no commit semantics: planners may probe with `ModelClient`, then
make exactly one selected call through `PlannerModelClient`. Terminal helpers
return the selected provider message without exposing transcript identity or
matching mechanics. Later session turns therefore replay the provider's signed
thinking while only the selected provider response becomes user-facing.
Retryable or rejected attempts never leak partial presentation. Usage events
still include every invocation. After atomic tool-batch admission, the workflow
commits the complete selected response once before any effects.

Provider adapters own reasoning-block validity. Streaming Bedrock and Anthropic
calls reject plaintext reasoning that closes without its provider signature;
canonical responses contain only finalized blocks and never fabricate redacted
placeholders. Bedrock and Anthropic also require every started content block to
close and an explicit stop reason before message completion, while Vertex
requires exactly one candidate with an explicit finish reason. Every adapter
exposes the canonical completed `model.Response` separately from the closed
typed presentation stream, preserving opaque replay metadata across gateways
without adding planner-visible events.

**Tool-call thought signatures**: some providers (for example, Gemini 3)
attach an opaque, provider-defined signature to a tool call that must be
replayed back verbatim on the next turn. The runtime captures these at the
model-client boundary — before either streaming style above ever produces a
`planner.ToolRequest` — and commits the isolated response candidate whose tool
call identities match the plan result. `planner.ToolRequest` never carries a
signature field; planners and custom `Planner` implementations do not need to
know signatures exist. Planners that hand-build `ToolRequest` values from a
`Complete` response should use `planner.ToolRequestFromModelCall`. The returned
request carries the provider correlation ID. Planner-authored requests have no
provider ID. In both cases the runtime assigns the execution ID.

---

## Tool Execution

### Tool Payload and Result Flow

1. **Model emits tool call** — Provider adapters produce a streamed or final tool call with canonical JSON bytes.
2. **Model boundary checks generated payload** — The call must name a tool in the exact request. Generated tools are decoded with their generated payload codec before planner code receives the call.
3. **Planner returns `ToolRequest`** — The planner supplies `Name` and canonical `Payload`. A request forwarded from a model call also carries `ModelToolCallID`; a planner-authored request leaves it empty.
4. **Runtime checks and compiles the request** — The activity decodes the selected payload with the registered generated codec, verifies any provider ID against the selected model response, and creates a runtime-owned `ToolCall` with a deterministic execution ID. Original model name and payload stay separate from executable planner intent.
5. **Executor runs tool** — Receives typed or raw payload depending on configuration.
6. **Runtime encodes result** — Uses generated codecs and persists canonical `ToolOutput` history.
7. **Planner resumes from `ToolOutputs`** — `PlanResumeInput.ToolOutputs` is the canonical execution-history boundary for budgeted tools only.

Bookkeeping tools follow the same execution and durability path, but not the
same accounting or planner-resume path: the runtime records their
hook/stream/run-log events and preserves their results in the provider
transcript while charging neither retrieval nor consecutive-failure budget.
Successful results are omitted from compact `ToolOutputs`; recoverable failures
remain visible for repair.

### ToolsetRegistration

Toolsets bundle execution logic for a group of tools:

```go
type ToolsetRegistration struct {
    Name        string                     // Qualified identifier (service.toolset)
    Description string                     // Human-readable context
    Metadata    policy.ToolMetadata        // Policy metadata
    Execute     func(ctx, *runtime.ToolCall) (*runtime.ToolExecutionResult, error) // Dispatcher
    Specs       []tools.ToolSpec           // JSON codecs and schemas
    TaskQueue   string                     // Optional queue override
    Inline      bool                       // Execute in workflow context
    CallHints   map[tools.Ident]*template.Template   // Tool call DisplayHint templates (typed payload only)
    ResultHints map[tools.Ident]*template.Template   // Success result preview templates (typed result only)
    ResultMaterializer ResultMaterializer  // Typed result enrichment before encoding
    AgentTool   *AgentToolConfig           // Agent-as-tool configuration
}
```

Generated `RegisterUsedToolsets` helpers remain the sole owner of each
service-backed toolset registration. Applications provide the required
executor with `With<Toolset>Executor`. When a typed result needs deterministic
application-owned enrichment before canonical encoding—for example, attaching
server-only display data—provide
`With<Toolset>ResultMaterializer` in the same registration call. Do not
register the toolset a second time.

### Tool Call Display Hints (DisplayHint)

The runtime can surface user-facing hints for tool calls (for example in UIs) via the `DisplayHint` field on
hook + stream events.

Contract:

- Hook constructors do not render hints. Tool call scheduled events default to `DisplayHint==""`.
- The runtime may enrich and persist a **durable default** hint at publish time. It first decodes the typed
  tool payload using generated codecs and executes the `CallHintTemplate` when registered.
- Tool registration requires a non-empty metadata title. When typed decoding fails or no template is
  registered, the runtime uses that title as the display hint. Malformed payloads still fail at the tool
  boundary; the metadata title only keeps the attempted work renderable. Hints are never rendered against raw
  JSON bytes.
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
- A **provider loop** runs inside the toolset-owning service process, subscribes to the toolset stream, executes the tool, and submits claimed-call results to the registry for publication on `result:<toolUseID>`

For method-backed service toolsets, codegen emits a provider adapter at:

- `gen/<service>/toolsets/<toolset>/provider.go`

That generated provider implements a dispatcher that decodes the tool payload JSON using generated codecs, adapts into the Goa method payload (via generated transforms), calls the bound method, and re-encodes the tool result JSON together with any declared server-data (optional observer-facing server-data and always-on server-only metadata).

To run it, wire the generated provider into the runtime provider loop:

```go
handler := toolsetpkg.NewProvider(serviceImpl)
providerID := podName + "/" + toolsetID
admissionRevision := mustRequiredEnv("TOOL_REGISTRY_ADMISSION_REVISION")
go func() {
    err := toolprovider.Serve(ctx, pulseClient, toolsetID, handler,
        toolprovider.Registration{
            AdmissionRevision: admissionRevision,
            Register: func(ctx context.Context, toolset, providerID, incarnationID, admissionRevision string) (toolprovider.RegistrationLease, error) {
                result, err := registryClient.Register(ctx, &genregistry.RegisterPayload{
                    Name:              toolset,
                    Description:       toolsetDescription,
                    Version:           toolsetVersion,
                    Tags:              toolsetTags,
                    Tools:             toolSchemas,
                    ProviderID:        providerID,
                    ProviderIncarnationID: incarnationID,
                    AdmissionRevision: admissionRevision,
                    WireProtocolVersion: registrywire.WireProtocolVersion,
                })
                if err != nil {
                    return toolprovider.RegistrationLease{}, err
                }
                return toolprovider.RegistrationLease{
                    RegistrationToken: result.RegistrationToken,
                    Duration:          time.Duration(result.LeaseDurationMs) * time.Millisecond,
                }, nil
            },
            Drain: func(ctx context.Context, toolset, providerID, incarnationID, expectedToken string, settlementDuration time.Duration) error {
                return registryClient.DrainProvider(ctx, &genregistry.DrainProviderPayload{
                    Name: toolset, ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                    ExpectedRegistrationToken: expectedToken,
                    SettlementDurationMs: settlementDuration.Milliseconds(),
                })
            },
            Release: func(ctx context.Context, toolset, providerID, incarnationID, expectedToken string) error {
                return registryClient.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
                    Name:                      toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    ExpectedRegistrationToken: expectedToken,
                })
            },
            Complete: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, requestEventID string, result registrywire.ToolResultMessage) error {
                resultJSON, err := json.Marshal(result)
                if err != nil {
                    return err
                }
                return registryClient.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
                    Toolset: toolset, ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                    RegistrationToken: result.RegistrationToken,
                    ToolUseID: result.ToolUseID, ResultJSON: resultJSON,
                    RequestEventID: requestEventID,
                    ProviderRegistrationToken: providerToken,
                })
            },
            PublishOutputDelta: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, callToken, toolUseID, requestEventID, stream, delta string) error {
                return registryClient.PublishToolOutputDelta(ctx, &genregistry.PublishToolOutputDeltaPayload{
                    Toolset: toolset, ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                    ProviderRegistrationToken: providerToken,
                    CallRegistrationToken: callToken, ToolUseID: toolUseID,
                    RequestEventID: requestEventID, Stream: stream, Delta: delta,
                })
            },
            ReportOverload: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, callToken, toolUseID, requestEventID string) error {
                return registryClient.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
                    Toolset: toolset, ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                    ProviderRegistrationToken: providerToken,
                    CallRegistrationToken: callToken, ToolUseID: toolUseID,
                    RequestEventID: requestEventID,
                })
            },
            Claim: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, callToken, toolUseID, requestEventID string) (toolprovider.ClaimDisposition, error) {
                result, err := registryClient.ClaimToolCall(ctx, &genregistry.ProviderToolCallClaimPayload{
                    Toolset: toolset, ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                    ProviderRegistrationToken: providerToken,
                    CallRegistrationToken: callToken, ToolUseID: toolUseID,
                    RequestEventID: requestEventID,
                })
                if err != nil {
                    return "", err
                }
                return toolprovider.ClaimDisposition(result.Disposition), nil
            },
        },
        toolprovider.Options{
            ProviderID: providerID,
            Pong: func(ctx context.Context, providerID, incarnationID, pingID string) error {
                return registryClient.Pong(ctx, &genregistry.PongPayload{
                    PingID:     pingID,
                    Toolset:    toolsetID,
                    ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                })
            },
        },
    )
    if err != nil && !errors.Is(err, context.Canceled) {
        panic(err)
    }
}()
```

This integration is intentionally split:

- **Registry gateway**: validates payloads and atomically owns provider-incarnation leases, health epoch/pong state, per-call publication admission, absolute expiration, and terminal result publication
- **Service provider loop**: executes tools using generated provider adapters and submits terminal results through the typed registry adapter

Import `goa.design/goa-ai/runtime/toolregistry` as `registrywire` in provider
and registry-consumer composition roots.
`runtime/toolregistry.WireProtocolVersion` is the only accepted registry
message-envelope version. Every provider Register payload and every consumer
CallTool or RetryTool payload must carry it. Consumers do not Register;
CallTool performs initial admission, while RetryTool can republish only one
existing exact admission after overload.

Provider leases are scoped by stable `ProviderID` plus a runtime-generated UUID
incarnation. `Serve` generates the incarnation once and passes it to typed
Register, Drain, Release, Claim, Complete, and Pong callbacks; deployment code never supplies or
infers it. A delayed old-process release therefore cannot delete its replacement.
Deployment
configuration supplies one required `AdmissionRevision` shared by every replica
in one fenced admission. Reuse it for scaling and same-contract RollingUpdate;
change it only when a new schema or rollout fence is intended. `Serve` passes
the revision and runtime identity to registration and Pong. It
opens the Pulse stream, waits for typed registration, and only then creates the
consumer-group sink. Calls published after admission but before sink startup
remain queued; an unadmitted process cannot claim them.
Read the revision once in the service composition root from required deployment
configuration such as `TOOL_REGISTRY_ADMISSION_REVISION`. Store the value on the
pod template and expose it to every replica of that admission. Do not rotate it
for a same-contract binary rollout. Do not derive the revision from pod names,
startup time, random process IDs, ReplicaSet hashes, filesystem metadata, or
runtime Kubernetes inspection. The accepted syntax is
`^[A-Za-z0-9][A-Za-z0-9._:/@+\-]{0,255}$`.
Transient registration failures use bounded exponential jitter. `Serve`
schedules successful renewal from the returned lease duration—approximately
one third into the lease with jitter—so short and non-round lease durations
retain time for retries. Every callback invocation
receives an attempt deadline and must return promptly when that context is
canceled because `Serve` waits for it during shutdown.

The registration callback must always submit the runtime-owned
`WireProtocolVersion`, the same generated schema payload, and the same
`AdmissionRevision` for one `Serve` lifecycle. The registry rejects a missing
or mismatched version with `validation_error` before stream creation, catalog
mutation, lease admission, or health scheduling. An identical wire
version/schema/revision registration renews only that provider's application
lease: it preserves
catalog identity metadata and token, exact-CAS updates the embedded lease, and
does not restart a healthy ticker. `RegisterResult` returns the active `RegistrationToken` and
`LeaseDurationMs`. `Serve` derives a conservative monotonic deadline from the
local attempt start, bounds retries by that deadline, and closes consumption
before it expires. A failed or ambiguous identical renewal preserves the prior
lease and is safe to retry. The single exact-CAS catalog record contains
active/retired state, toolset metadata, wire protocol version, schema
fingerprint, admission revision, token, Redis `RegisteredAt`, every
provider-incarnation lease, health epoch, last pong, and the exact set of all
retired registration tokens. Whenever
the routable provider set transitions between empty and nonempty, CAS advances
the epoch and clears pong freshness. Draining or releasing an already
non-routable lease does not advance it. Ping IDs carry token plus epoch; Pong atomically authenticates that
pair and the responding incarnation. Aggregate health requires one unexpired,
non-draining embedded lease plus one fresh current-epoch pong; it does not require every
replica to pong. Registry-issued leases are at least 45
seconds—longer than the default shutdown, attempt, and retry budget—and at most
24 hours. Providers additionally reject any returned lease that does not exceed
their configured shutdown margin plus bounded attempt and retry delay.

A different token prunes leases using Redis TIME. While an old lease remains,
`Register` returns retryable `admission_blocked`; once none remain, exact CAS
replaces the record with the candidate and first lease. A blocked initial
registration retries with jitter. A blocked renewal means the provider is
superseded, so `Serve` immediately stops claiming work instead of waiting for
its local lease cutoff. Replacement atomically adds the prior token to the
catalog's permanent tombstone set. Retirement does the same for the current
token. Any later A candidate after A→B receives permanent
`admission_retired`; a fresh revision derives a new token. This exact set is
permanent unbounded correctness history and grows with distinct admissions.
Do not truncate or probabilistically compact it.

After every admitted exit, `Serve` stops renewal, atomically marks every exact
token/incarnation lease draining, then closes the shared sink before processing
the remaining local queue. Draining excludes that incarnation from new
publication but preserves its authority to claim and complete request events
already delivered before intake closed. The Drain request carries the
configured shutdown duration plus `SettlementAuthorityMargin`, and Redis
extends the lease from the transition time through that authority window.
`Serve` applies one shared `ShutdownTimeout` deadline to sink closure, worker
and registry-owned terminal settlement, and queued acknowledgement drain. The
lease margin keeps authority valid while the final operation crosses the
transport boundary. `ShutdownTimeout` therefore cannot exceed
`MaxShutdownTimeout`, which reserves that margin inside
`MaxProviderLeaseDuration`. A
sink-creation failure has no consumption to settle and proceeds directly to
bounded release. Only a proven clean settlement releases the exact provider
lease. A sink-close, worker, result-publication, or acknowledgement failure is
returned and suppresses release so lease expiry can converge safely. A stale
release token or missing lease is an idempotent success.
Before each handler invocation, `Serve` calls `ClaimToolCall`. The registry
authenticates the exact provider lease and request event, then atomically grants
one immutable dispatch owner or returns `terminal`, `claimed`, or `expired`.
Only `execute` invokes the handler. Terminal and claimed redeliveries are
acknowledged without repeating side effects; a crashed owner is never replaced.
Each claim enters global and exact-lease durable indexes. The absolute execution
deadline, exact provider release, or lease loss atomically commits an `internal`
/ `outcome_unknown` terminal stating that the effect may have occurred. The
terminal is committed before the later call-record retention expiration. The
same operation publishes the canonical stale-registration terminal for an
unclaimed old-generation request. Redis owns liveness.
Handlers still must honor context cancellation and return promptly; otherwise
`Serve` reports worker settlement failure and withholds lease release.

Use RollingUpdate for provider releases. Replicas with the same token overlap
normally. When the schema or admission revision changes, the replacement pod
retries registration while the old admission drains, so the two admissions do
not execute concurrently. Calls that arrive after the old route stops wait
inside `CallTool` until the replacement is healthy; that wait consumes the
call's existing execution deadline and does not create a model-visible retry.
Request publication rechecks the provider in the same Redis operation that
appends the call. If the old provider started draining after the health check,
the unpublished call selects the replacement and tries again within the same
deadline. A published call never moves to another provider.
A registry cannot distinguish a release handoff from another interval with no
healthy provider, so the same bounded wait applies to both rather than guessing
from version or process state.
A crash may extend the wait until the old lease expires. A wire protocol change
remains a coordinated hard cutover because registry servers and consumers do
not negotiate envelope versions. No deployment component persists registration
tokens or calls `Unregister` during rollout.
`Unregister` is reserved for
intentional retirement: exact active becomes retired while preserving leases,
same-token retry succeeds, stale token returns `admission_conflict`, and the
same retired token returns permanent `admission_retired` from `Register`.

Do not overlap different admission generations on the same toolset stream.
The registry persists `WireProtocolVersion`, `SchemaFingerprint`, and
`AdmissionRevision` separately.
The registration token is the lowercase SHA-256 digest of
`goa-ai/tool-registry-admission/v2\0`, the uint32 big-endian wire protocol
version, the raw 32-byte schema fingerprint, a uint32 big-endian
admission-revision byte length, and the revision bytes. It is
identity, not a secret, and every API and Pulse boundary requires the canonical
`^[0-9a-f]{64}$` spelling. Tool order and toolset/tool tag order are normalized,
while payload, result, and sidecar schema bytes are hashed exactly. Generated
raw schemas must therefore be canonical: reformatting semantically equivalent
schema JSON changes schema and admission identity.

This version is a breaking-wire fence, not capability negotiation. There is no
legacy ToolError envelope, optional fallback, or dual decoder. Quiesce
consumers, drain admitted calls and provider leases, then stop every old
registry replica. Back up the catalog and atomically remove entries owned by
the old wire version while preserving retained call records. Start the new
registry against that cleaned catalog, then start matching providers and
finally consumers.

The new registry rejects an old consumer's missing version (protobuf zero) at
the generated transport boundary and repeats the check at the first line of
CallTool or RetryTool, before catalog lookup, health checks, result-stream
creation, call admission, or Pulse publication. It likewise rejects an old
provider on Register or renewal before provider admission. The same
runtime-owned version therefore fences both producers of protocol bytes while
the version-bound registration token continues to fence provider generations.
Incoming `run_id`, `session_id`, `turn_id`, `tool_call_id`, and
`parent_tool_call_id` values are bounded to 256
bytes and reject NUL at the generated transport boundary; provider and
incarnation IDs reject NUL as well. An invalid identifier is rejected before
any tool-call publication.
Every accepted planner request receives a runtime execution ID equal to `call-`
plus lowercase SHA-256 over `goa-ai/runtime-tool-call-id/v1\0` and
length-delimited run ID, turn ID, attempt, batch index, and tool name. The
opaque ID is deterministic across workflow replay and changes when any identity
input changes. A provider's own correlation ID remains separate.
The gateway requires `tool_call_id` and derives the global transport
`tool_use_id` as lowercase SHA-256 over the domain
`goa-ai/tool-registry-use/v1\0` plus uint64-length-delimited `run_id` and
`tool_call_id`. Retries of one run/call reuse the ID, while equal model call IDs
in concurrent runs cannot collide. The model/provider `tool_call_id` remains
unchanged in metadata. Direct callers must generate and retain a stable call ID.
Every routed call carries the exact active registration token used for
validation and health admission. `ClaimToolCall` publishes the canonical typed
`stale_registration` result under an unclaimed older call token before the
provider acknowledges it. Preserved draining or retired exact leases may still
complete calls they already claimed. Executors map stale registration to
retryable tool-unavailable. Every provider
success, error, and best-effort output delta echoes the call token. Executors
forward deltas and accept terminal results only for the
exact `(tool_use_id, registration_token)` pair; mismatched late events from a
reused tool-use ID are ignored by each independent replay reader.

`MaxQueuedToolCalls` is the exact waiting bound and provider concurrency/queue
settings reject negative or excessive allocation. When that queue is full, the
provider reports the claimed request to the registry. The registry atomically
publishes a top-level retry-control variant with reason `provider_overloaded`
and a bounded retry-after only while the call remains nonterminal, then the
provider acknowledges the request. Retry control carries no `ToolFailure`; terminal
`ToolError` is the only planner-failure envelope. The executor calls the
distinct RetryTool operation with the original `ToolCallRef.RegistrationToken`.
The registry verifies that token is still active, attaches to the existing
immutable call admission, reads the retained overload event from that call
record without snapshotting Pulse, and atomically republishes at most once for
each overload event across nodes. Reporting overload is itself idempotent for
the exact request event. A missing admission or changed active
token fails before publication; retry never binds to a replacement provider.
The bounded publish operation never blocks provider ping intake. Exhausted or failed
registry/Pulse infrastructure maps to retryable tool-unavailable.
`stale_registration` remains a retryable terminal outcome.

Call publication and result retention have one owner. The registry atomically
stores one record for each global `tool_use_id`. The record contains an
immutable token-independent request digest and an explicit `admitted` or
`rejected` state. Before initial publication, an admitted record's provider
token may move to the current healthy registration because Redis proves no
provider received the call. Publication checks and fixes that token in the same
operation that appends the request. A rejected record preserves the exact typed
pre-publication error, so retries cannot execute while the record is retained.
`CallTool` reads the record before consulting current routing or health, so
rejected calls replay their error and published calls return their original
token and deadlines after retirement, replacement, or temporary loss of
current health. CallTool owns initial admission and publication. RetryTool owns
overload republication and requires the published admission plus its original
still-active token; it cannot create missing admission state. Each initial or
overload request append and its publication marker commit in one Redis
operation. Concurrent attempts and retries
after an ambiguous Redis response therefore resolve to the original request
event instead of appending a duplicate; publication ownership cannot expire
mid-operation. The call record computes a Redis-time absolute execution
deadline from `TOOL_EXECUTION_TIMEOUT` (at most ten minutes) and a later
absolute expiration from configured retention (11 minutes to 24 hours, 15
minutes by default). An invalid `TOOL_EXECUTION_TIMEOUT` duration fails registry
startup instead of selecting the default. `CallToolResult.execution_deadline` and
`ToolCallMessage.execution_deadline_unix_ms` carry the execution deadline;
`result_stream_expires_at` and `result_stream_expires_at_unix_ms` carry
retention. Call admission and every bounded `result:<toolUseID>` handle share
the retention expiration. Pulse
retains at most `ResultStreamMaxLen` events, while the call record stores the
full canonical terminal and restores its delivery event after trimming.
Providers submit
terminal results, overload notices, and output deltas through typed registry
operations. Each operation verifies the exact token/incarnation lease and the
specific request-stream event claim. A preserved retired or draining exact
lease may settle already-published work it owns.
`CompleteToolCall` atomically appends the canonical result and commits terminal
state; later overload notices and deltas become no-ops. Output fragments are
bounded by `MaxToolOutputDeltaBytes` and
`MaxToolOutputDeltaCount` per call. Provider handler context, output, and
completion are bounded by the execution deadline. If a claimed call has no
terminal then, the registry atomically commits `outcome_unknown` while the
authoritative call record is still retained. Exact terminal repeats return the
original event while replay restores a missing event from the call record.
Executors subscribe with
independent oldest-first Pulse Readers; sequential and concurrent waiters each
replay immutable history and create no consumer-group, acknowledgement, pending,
or keepalive metadata. Registry retry decisions use call-record overload state
and never snapshot Pulse history. The executor validates timestamp structure
without applying its local clock, then waits through the returned execution
deadline. Ambiguous admission, retry, or result-stream setup failures finish
with `outcome_unknown` guidance and never authorize a replacement call. The
minimum retention leaves a one-minute transport margin. Readers close but never destroy
the stream. Publication preserves rather than extends the absolute deadline, so
the terminal call record cannot expire before its retained result and a reused
tool-use ID cannot republish while either exists. Completed, abandoned,
duplicate, and recreated state expires without a separate cleanup protocol.

Wire protocol version 8 introduces the explicit decision state. Registry
replicas running versions 7 and 8 must never overlap. Follow the hard-cutover
order above; version 8 cannot start while version 7 catalog entries remain. On
first read, version 8 validates the complete retained version 7 call-admission
shape, adds the `admitted` discriminator without extending its TTL, and rejects
malformed records. Unobserved version 7 call records expire at their original
deadline.

Toolsets with no routable leases have no health identity, so the scheduler emits
no pings and an idle request stream cannot grow. Active request streams trim
only below the minimum safe consumer-group watermark:
the earliest pending ID for a group with PEL entries, otherwise that group's
last-delivered ID. It never uses length trimming, because acknowledged later
traffic cannot make an older pending call disposable.
Malformed exact terminal events fail immediately as
protocol errors instead of being skipped until timeout. Providers stamp the
call identity and validate the strict success/error/retry union before publishing
or acknowledging a handler result.

Recovery has two owners. Pulse owns recovery of an admitted provider sink.
`Serve` registration reconciliation owns recovery of the catalog-owned lease,
and the registry's ensure-only ping loop after temporary registry or Redis
state loss. Supported rmap destruction, stream destruction, and ticker loss
recover live under the same registry name without a process restart; the
reconstructed ping/pong exchange restores group-health freshness.

A raw total Redis reset is not that supported path: it erases rmap revision
history and the catalog's permanent retirement tombstones. Canonical schema and
revision still derive the same token, but live recovery and anti-resurrection
cannot be guaranteed from empty Redis. Stop incompatible admissions and restore
a catalog backup or deliberately rebootstrap the registry before resuming
providers. Never overlap incompatible admissions.

Pre-contract Redis records and unfenced queued messages require a one-time
operational hard cutover. Follow
[`POST_ROLLOUT_CLEANUP.md`](POST_ROLLOUT_CLEANUP.md); do not remove the permanent
catalog lease, token-fencing, flat-stream, or non-overlap mechanisms after the
cleanup.

### Registry-Routed Execution (Agent/Consumer Side)

On the consumer side (an agent calling registry-routed toolsets), the runtime needs a `ToolCallExecutor` that:

- calls the registry gateway to publish the tool request and get `(tool_use_id, registration_token, execution_deadline, result_stream_expires_at)`, then
- subscribes to the retained per-call result stream but waits only through the absolute execution deadline before decoding the result using the compiled tool specs/codecs.

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
    Execute: func(ctx context.Context, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
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
    Labels            map[string]string // Run labels plus runtime-authored values for this call
}
```

`Labels` exposes server-authored context to custom executors without placing
those values in the model payload. Terminal finalization calls include the
exact reason under `runtime.FinalizationReasonLabel`; ordinary calls do not.
Mutating this map does not change the workflow's saved labels or another tool
call.

### Injected Fields (`Inject()`)

`Inject(names...)` in a `Tool` block marks payload fields as server-populated:
hidden from the model (excluded from the JSON schema and the model-facing
required list) and filled in by generated code before the tool executes.
Injection is **compiled at generation time**, not interpreted at runtime --
there is no reflection, no spec-walking, and no generic map plumbing beyond
the labels map itself.

**Design → codegen → runtime flow:**

1. Design time: `Inject("session_id")` / `Inject("household_id")` records the
   field name(s) on the tool. DSL `Validate` resolves each name against the
   tool's effective payload (the explicit `Args()` when given, otherwise the
   bound method's payload) and rejects the design if the field is missing,
   optional, non-`String`, or (for a `BindTo` tool) label-backed.
2. Codegen time: each name is classified against the fixed
   `runtime.ToolCallMeta` field set (`sessionId`/`session_id` -> `SessionID`,
   `runId`/`run_id` -> `RunID`, `turnId`/`turn_id` -> `TurnID`,
   `toolCallId`/`tool_call_id` -> `ToolCallID`,
   `parentToolCallId`/`parent_tool_call_id` -> `ParentToolCallID`). A match is
   **meta-backed**; anything else is **label-backed**, using the design name
   verbatim as the label key. `codegen/agent/prepare.go`'s `flattenAndHide`
   removes the field from the model-facing schema and required list, which
   makes every injected field a **pointer** on the generated tool payload
   struct. Each toolset's `inject.go` (beside its `codecs.go`/`specs.go`) gets
   one generated `Inject<Tool>(p *<Tool>Payload, meta runtime.ToolCallMeta,
   labels map[string]string) error` function per injecting tool: meta-backed
   fields copy directly from `meta`; label-backed fields look the key up in
   `labels`, run the field's own declared validation (reusing Goa's
   attribute-validation codegen, not hand-duplicated), and return a precise
   error naming the tool and key on a missing or invalid label. The toolset's
   `RequiredLabels` (sorted, deduplicated label keys) is generated onto its
   specs package and aggregated, per agent, onto `AgentRegistration`.
3. Runtime time: both execution topologies call the **same** generated
   `Inject<Tool>` function between decode and execute, so population never
   diverges by where a tool runs:
   - Local (in-process) execution: the generated service executor calls
     `Inject<Tool>` immediately after decoding the tool payload, before any
     `WithPayloadMapper` customization or method-payload conversion, using
     the run's `ToolCallMeta` and runtime-owned `ToolCall.Labels`.
   - Registry-served (bound) tools: the generated provider (`provider.go`)
     calls the same `Inject<Tool>` function with a `nil` labels map --
     sound only because a `BindTo` tool can never declare a label-backed
     field (rejected at design time), since the registry wire protocol
     carries no run labels.
   - Custom (hand-written) `ToolCallExecutor`s -- for tools with no `BindTo`,
     registered directly with the runtime -- have no generated call site.
     **Use the toolset's generated `Decode<Tool>(payload []byte, meta
     runtime.ToolCallMeta, labels map[string]string) (*<Tool>Payload, error)`
     function to decode these tools' payloads**, not the raw
     `<Tool>PayloadCodec.FromJSON` followed by a manual `Inject<Tool>` call.
     `Decode<Tool>` composes both in one call; decoding with the codec alone
     silently leaves injected fields at their Go zero value, because their
     wire tag is `json:"-"` and there is no "missing key" signal. `payload`
     accepts a `runtime.ToolCall.Payload` (`rawjson.Message`) directly.
4. Run start: `Runtime.Start`/`StartOneShot` (and their route variants)
   validate the caller-supplied `WithLabels(...)` map against the starting
   agent's aggregated `RequiredLabels` **before** scheduling any workflow or
   activity, failing fast with every missing key named in one error.
   **Gateway/orchestration no-op:** this check reads `RequiredLabels` off the
   *locally registered* `AgentRegistration`. A process that only holds a
   `Runtime.ClientFor(route AgentRoute)` (or `MustClientFor`) -- the pattern
   this runtime documents for gateway and orchestration processes that do
   not run the agent's workflow themselves -- has no local registration, so
   the check is a silent no-op there; a missing label is instead caught
   later, per tool call, when `Inject<Tool>` actually runs. Carry
   `RequiredLabels` on `AgentRoute` yourself if this gap matters for your
   deployment; the runtime does not do so today.
   **Agent-as-tool child runs** bypass run-start validation the same way:
   `ExecuteAgentChildWithRoute` starts the child workflow directly (no
   `Start`/`StartOneShot` funnel), so the child's `RequiredLabels` are never
   checked up front. The parent run's labels propagate to the child
   unchanged (the child's `RunInput.Labels` is a copy of the parent tool
   call's labels), so a parent started with the right `WithLabels(...)`
   satisfies the child too; a label the child needs that the parent never
   carried fails loud at the child's `Inject<Tool>` call, per tool call,
   not at child start.

**`WithLabels` contract:**

```go
out, err := client.Run(ctx, sessionID, messages,
    runtime.WithLabels(map[string]string{"household_id": "house-42"}),
)
```

`WithLabels` merges into `RunInput.Labels`, which the runtime copies into `ToolCall.Labels`
unchanged across both engines (inmem and Temporal) down to every tool
execution in the run. The same labels come back out at the end of the run:
the terminal `RunCompletedEvent.Labels` and `run.Snapshot.Labels` carry the
start labels so completion hooks and `GetRunSnapshot` readers can recover the
run identity without out-of-band tracking. Labels are plain strings; **only `String` fields may be
injected** -- there is no generated conversion to numeric, boolean, or
structured types. A design that needs a non-string injected value must model
it as a `String` (with `Pattern`/`Format` validation as needed) and convert
in service code.

`goa-ai.finalization_reason` is the exception to unchanged tool-call labels.
The runtime removes caller, policy, planner, and model values for this reserved
key. It writes the exact termination reason only when executing a terminal
finalization tool call.

**Example (label-backed):**

```go
Tool("lookup_household", "Lookup scoped to a household", func() {
    Args(func() {
        Attribute("household_id", String, "Household to scope the search to.", func() {
            Pattern("^[a-z0-9-]+$")
        })
        Attribute("query", String, "Search query.")
        Required("household_id", "query")
    })
    // household_id is not a ToolCallMeta field, so it is label-backed:
    // the run must supply it via WithLabels("household_id", "...").
    Inject("household_id")
})
```

See `codegen/agent/tests/testscenarios/inject_examples.go` (`InjectLabelExample`,
`InjectBoundMetaExample`, `InjectMultiToolsetLabelsExample`,
`InjectMixedBoundUnboundExample`) for complete, generation-tested design
shapes exercising both meta-backed and label-backed injection, RequiredLabels
aggregation across toolsets, and the mixed bound/unbound-tool case.

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

`tools.ToolSpec.Bounds` stores model-facing JSON field names. DSL declarations
may refer to lower-camel Goa attributes such as `nextCursor`, but generated
schemas, runtime projection, and retry guidance use the JSON name
`next_cursor`.

The runtime enforces one strict contract across all result ingress paths
(regular execution and externally provided await results):

- unbounded tools must not return bounds metadata,
- error tool results must not return bounds metadata,
- successful bounded results must include bounds metadata,
- when `truncated=true`, bounds must include either `next_cursor` or
  `refinement_hint`.

When `tools.ToolSpec.Bounds.Paging` names a dedicated continuation, the runtime
advances compatible zero-item results with a non-empty next cursor before
invoking the planner. The continuation is fully determined until the chain
returns evidence or exhausts. For a result containing one or more items, the
runtime advertises a temporary action that uses the generated empty-object
continuation schema and describes the original model-visible query. Before
execution, the runtime maps the selected action to the canonical continuation
tool, binds that query's source tool-call identity and opaque cursor, and
retains any required canonical query fields. The model-authored action name and
empty payload remain separate for transcript replay. A planner batch may
advance several independent actions, but may call each action at most once. A
continuation result that repeats its input cursor is rejected because it cannot
make progress.

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
own truncation logic; the runtime propagates their bounds and advances only
zero-item dedicated continuations whose next action is mechanically known.

For a dedicated continuation, `Bounds.NextCursor` remains runtime-owned and is
not projected into the model-visible result. The runtime exposes a distinct
empty-input action per live chain after that chain returns at least one item and
includes the original model-visible query in that action's description. It
binds the selected chain's exact cursor and, when configured, the originating
query payload before execution. The durable scheduled-call record carries the
source tool-call identity, so completing or advancing one chain does not
consume another chain even when their cursor bytes are equal.

For a self-paging tool declared with `Cursor`, `Bounds.NextCursor` is projected
into the configured result field and the model supplies it in the next call.
Use that contract only when repeating the query arguments and opaque cursor is
part of the public tool interaction rather than mechanical continuation state.

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
method result, `returned` and `truncated` are required. `total` may also be
required when the service always computes exact cardinality; otherwise it is
optional. `refinement_hint` and `next_cursor` remain optional and are omitted
from emitted JSON whenever runtime bounds omit them. `BoundedResult(...)` still
owns the tool-facing contract exposed to models.

Code generation marks exact-total tools in `tools.BoundsSpec`, requires `total`
in their model-visible result schema, and rejects a successful runtime result
whose bounds omit it.

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

## External Input and Workflow Continuations

Each accepted user input starts one top-level workflow for that turn. The
workflow ends with either that turn's final result or an external-input
suspension. Nested agents still run as linked child workflows.

Clarification, confirmation, structured questions, and external tool requests
end the current workflow successfully. The returned `api.RunSuspension`
contains the visible pending requests plus a private checkpoint. No workflow
waits while a person or external system prepares the answer.

Before completing the workflow, the runtime persists the suspension in its
configured session store under the completed run ID. The checkpoint can contain
private planner messages and tool state; do not send it to an untrusted client.
The owning service must atomically accept one answer before starting the
continuation, so concurrent answers cannot start two workflows from the same
state. When one answer is ready, start a new workflow with the completed run ID,
a new run ID, and a new turn ID:

```go
next, err := client.Continue(
    ctx,
    "session-1",
    previous.RunID,
    "run-124",
    "turn-2",
    response,
)
```

One continuation consumes only the first item in `Suspension.Pending`. If more
input remains, the new workflow ends with another suspension. The checkpoint
restores the original messages, policy, labels, nested-tool identity, remaining
active-time budget, and exact call/result provenance; callers cannot override
those values. The runtime loads the suspension by predecessor run ID and checks
the checkpoint version, public pending requests, and required tool names before routing. The receiving
worker restores saved payloads and results through its current generated codecs;
there is no cross-release compatibility promise, and any saved value outside
the current contract fails at that typed boundary. If the response closes a tool call created by the previous
workflow, the `tool_end` event belongs to the new result run and its required
`call_run_id` identifies the run that emitted the matching `tool_start`.

### Coordinated Generated-System Releases

Generated agents, generated completion packages, runtime workers, and their
callers use one contract and deploy as one release unit. Goa-AI does not provide
backward compatibility, mixed-version operation, or suspension migration for
generated runtime contracts.

For a release that changes generated or persisted runtime shapes:

1. Regenerate every consumer from the same Goa-AI revision.
2. Deploy the runtime workers, generated packages, and callers as one release.
3. Verify every deployed component reports the same revision ready.

The deployment does not drain or migrate work created by the previous release.
Ongoing workflows and saved suspensions may fail when they reach the new
contract. Historical completed-session records remain stored unchanged; this
release policy does not alter their persistence schema.

`goa-ai.run-suspension.v3` is the only supported suspension schema. The runtime
rejects older persisted shapes; it does not infer missing fields, migrate them,
or provide a compatibility execution path.

### Upgrade checklist for strict model-output contracts

This release makes the model boundary reject malformed output instead of asking
the model for a replacement. It also changes source and workflow contracts:

- `model.Client` is now package-owned and `Client.Stream` returns
  `*model.ValidatedStream`. Replace custom `Client` implementations with
  `model.Provider`, pass the provider to `model.NewClient`, and install
  provider-side wrappers with `model.WrapClient`. Direct provider callers use
  `model.NewRequestContract`; ordinary callers use the opaque client.
- `planner.ConsumeStream` accepts that validated stream directly. Remove stream
  wrappers and caller-side validation helpers. Stream observers may inspect
  `Response()` during callbacks but must not call `Recv()` or `Close()` on the
  same stream; lifecycle operations wait for the active callback.
- Temporal engines now require `ClientOptions` and always install the Goa-AI
  data converter. Remove `Options.Client` and custom `DataConverter` wiring;
  construction rejects a non-nil custom converter.
  Every workflow and activity call has one aggregate 1 MiB encoded limit.
  Tool executors must persist larger domain results before returning and place
  their durable reference in the typed result; the runtime never truncates or
  infers a replacement.
- `planner.ToolRequest` now contains `Name`, `Payload`, and optional
  `ModelToolCallID`. Use `planner.ToolRequestFromModelCall` when forwarding a
  validated provider call; leave the provider ID empty for planner-authored
  requests.
  Tool executors receive `*runtime.ToolCall`, which contains runtime-assigned
  labels and execution identifiers. Replace the removed `CallOption` API with
  `planner.NewToolRequest(gentool.<Tool>Tool(), args)`. The runtime assigns each
  call ID.
- Rate-limit middleware construction now returns `(model.Client, error)`.
  Check and propagate the setup error instead of treating `Middleware(...)` as
  an infallible client value.
- Gateway `NewRemoteClient` now returns `(model.Client, error)`. Check the
  constructor error before registering or invoking the client.
- Provider adapters no longer expose concrete clients as the model boundary.
  Implement `model.Provider`, construct the validated client with
  `model.NewClient`, and install middleware with `model.WrapClient`.
- Generated tools must use `model.ToolDefinitionFromSpec` so the generated
  payload decoder runs before planner code sees a tool call. Caller-authored
  tools use `model.AdvertisedToolInputFromSchema`, which compiles their schema.
  `model.ToolInputFromContract` reconstructs provider-facing schema documents
  after transport; it does not make a transport projection eligible for use as
  a validated client request.
- Generated completion packages keep codec-bearing specs private. Replace
  direct `completion.Complete` / `completion.Stream` calls that passed
  `Spec<Name>` with generated `Complete<Name>` / `StreamComplete<Name>`
  wrappers. Keep unary value reads on `Response.Value`, replace
  `Response.Attempts[0]` with `Response.ModelResponse`, and use
  `<Name>Example()` only for authored example JSON. Regenerate generated agents
  and completion packages before compiling callers.
- Invalid model output, invalid planner output, and a planner closing a model
  stream before EOF end the run with `OutputContractError`. The runtime does not
  make a correction inference or execute later tool calls. Temporal stores this
  failure as `goa_ai.output_contract_error`; the earlier planner-specific type
  is not decoded as a compatibility alias.
- Planner activity results and accepted planner-event records have new Temporal
  payload shapes. Suspension schema `goa-ai.run-suspension.v3` is required and
  is decoded with exact numbers, unknown-field rejection, and no trailing data.
- Child workflow IDs now append the exact runtime tool-call ID to the nested
  agent run path. Existing in-progress child workflows are not compatible with
  the new ID derivation.

Regenerate all generated agents and completion packages, then deploy them with
the runtime as described in [Coordinated generated-system
releases](#coordinated-generated-system-releases). Do not overlap old and new
generated contracts or add decoding for the replaced payload shape. No database
migration is required.

Ending a session stops future work but retains its run metadata for inspection.
When the owning application permanently deletes the session's customer data, it
must wait for in-flight workflow and stream work to settle and then call
`Runtime.PurgeSession`. Purging removes the session, every owned run record, and
all private checkpoints. It is idempotent and rejects an active session.

### Clarification Requests

Runtime-owned flows can request missing information that resumes as a user
message:

```go
return &planner.PlanResult{
	Await: planner.NewAwait(
		planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID:            "clarify-device",
			Question:      "Which device should I configure?",
			MissingFields: []string{"device_id"},
		}),
	),
}
```

The runtime emits an `AwaitClarification` event and returns a suspension.
Callers start the next workflow with:

```go
response := &api.PendingInputResponse{
    Clarification: &api.ClarificationAnswer{
        ID:     "clarify-device",
        Answer: "Device ID is ABC-123",
    },
}
```

When a model-authored tool collects free text, use the distinct tool-bound
clarification branch. It preserves the exact provider call across the workflow boundary and
correlates the human answer as that call's generated `{\"answer\": string}`
result:

```go
return &planner.PlanResult{
	Await: planner.NewAwait(
		planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
			ID:         "clarify-device",
			ToolName:   tools.Ident("chat.ask_clarification"),
			ToolCallID: call.ToolCallID,
			Payload:    call.Payload,
			Question:   "Which device should I configure?",
		}),
	),
}
```

The runtime emits the same `AwaitClarification` event for the UI. The next
workflow decodes the answer with the registered generated result codec and
restores a provider-valid `tool_use` / `tool_result` pair. Do not replace this
correlation with a reminder or a copied user message.

### External Tools

Planners can request tools that execute out-of-band:

```go
return &planner.PlanResult{
    Await: planner.NewAwait(
        planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
            ID: "external-1",
            Items: []planner.AwaitToolItem{{
                Name:       tools.Ident("external.fetch"),
                ToolCallID: "tc-ext-1",
                Payload:    json.RawMessage(`{"url":"..."}`),
            }},
        }),
    ),
}
```

Callers start the next workflow with the exact result set:

```go
response := &api.PendingInputResponse{
    ToolResults: &api.ToolResultsSet{
        ID: "external-1",
        Results: []*api.ProvidedToolResult{
            {
                ToolCallID: "toolcall-1",
                Name:       tools.Ident("chat.ask_question.ask_question"),
                Success: &api.ProvidedToolSuccess{
                    // Canonical JSON matching the tool's Return schema.
                    Result: json.RawMessage(`{"answers":[{"question_id":"...","selected_ids":["approve"]}]}`),
                },
            },
        },
    },
}
```

Provided tool results are strict boundary inputs:

- each item must contain exactly one `Success` or `Failure` outcome,
- `Success.Result` is canonical JSON matching the registered result codec,
- `Failure` supplies the classification, message, and requested recovery
  action; the runtime derives correction metadata from the registered tool
  spec instead of accepting caller-authored schema guidance,
- if the tool is bounded and successful, `Success.Bounds` must be present and satisfy
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
- Ends with a suspension containing that confirmation request.
- Executes the tool in the continuation workflow only when approved.
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

- **Continuation response**:

  ```go
  response := &api.PendingInputResponse{
      Confirmation: &api.ConfirmationDecision{
          ID:          "await-1",
          Approved:    true, // or false
          RequestedBy: "user:123",
          Labels:      map[string]string{"source": "front-ui"},
          Metadata:    map[string]any{"ticket_id": "INC-42"},
      },
  }
  ```

Consumers should treat confirmation as a **runtime protocol**, not as a
user-defined tool. Render the first `Suspension.Pending` item when its kind is
`confirmation`; do not couple UI behavior to a specific tool name.

This keeps the runtime generic: any UI/system can implement a compatible confirmation transport.

### Tool authorization events

When the continuation consumes a decision, the runtime emits a first-class authorization event:

- **Hook event**: `hooks.ToolAuthorization`
- **Stream event type**: `tool_authorization`

This event is emitted exactly once per confirmed tool call and captures the durable authorization record:

- `tool_name`: the tool being authorized
- `tool_call_id`: the tool call identifier
- `approved`: true/false decision
- `summary`: deterministic runtime-rendered summary (derived from the confirmation prompt)
- `approved_by`: copied from `api.ConfirmationDecision.RequestedBy` and intended to be a stable principal identifier (for example, `user:<id>`)

The event is emitted immediately after the decision is received:

- **Approved**: emitted before the tool executes.
- **Denied**: emitted before the denied tool result is synthesized.

Consumers (UIs, audit stores, session recorders) should rely on `tool_authorization` for “who/when/what” rather than inferring authorization from tool results.

**Runtime validation**

The runtime treats confirmation as a boundary and validates:

- The confirmation `ID` exactly matches the first pending identifier.
- Exactly one continuation response variant is present.
- `RequestedBy` is non-empty.

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
| `RunStarted` | Run begins (carries `RunContext`, including run labels) |
| `RunCompleted` | Run finishes (success, failed, canceled); carries the run's start labels |
| `RunSuspended` | Workflow ended with a versioned checkpoint and ordered pending input |
| `RunPhaseChanged` | Phase transitions (planning, executing_tools, etc.) |
| `PromptRendered` | Runtime resolves and renders a prompt spec |
| `ToolCallScheduled` | Tool activity scheduled |
| `ToolResultReceived` | Tool completes |
| `ToolCallUpdated` | Parent tool discovers more children |
| `AssistantMessage` | Final assistant response |
| `PlannerNote` / `ThinkingBlock` | Planner reasoning |
| `AwaitClarification` / `AwaitExternalTools` | External-input requests |
| `PolicyDecision` | Policy evaluation result |
| `Usage` | Token usage report |
| `ModelOutputRejected` | Fingerprints a model-output rejection before execution or presentation |
| `PlannerOutputRejected` | Fingerprints a planner result rejected after model output was accepted |
| `ChildRunLinked` | Agent-as-tool child run link |

Every response or stream rejected by the model boundary attempts to publish
this event. A planner result rejected after model calls finish does not publish
a model-output event. A durable publication failure is fingerprinted and joined
to the terminal `planner.OutputContractError`; it does not make inference
retryable.
`ModelOutputRejectedEvent.ReasonSHA256` and `ReasonSize` identify the exact local
validation error without copying provider-controlled text into Temporal or the
run log. `ModelResponsePresent` distinguishes a complete response from a
chunk-level failure. `ModelResponseFingerprintVersion` identifies the stable
encoding when `ModelResponseSHA256` is present; both are empty when no digest
could be computed. `ModelResponseSHA256` identifies the complete response from
the earliest-started model call that rejected output, and `ModelResponseSize`
reports its encoded bytes. Version 1 encodes the raw complete response before
any ownership copy and covers every `model.Response` field and closed
`model.Part` variant, including malformed raw tool bytes that JSON cannot
represent. Ordinary Go field reordering therefore does not change the digest;
metadata struct fields are ordered by name, and their tags and anonymous-field
status remain part of the identity. Empty digest and zero size with
`ModelResponsePresent=true` mean a complete response used metadata outside the
encodable contract; the terminal rejection is still recorded without treating
it as a missing response. Valid numeric counts in a rejected usage chunk remain
part of aggregate usage, while the invalid model identity is discarded. Raw
model content is not part of the run-log contract; deployments that need it
capture it through provider observability rather than Temporal payloads, hook
records, or a second runtime-owned content store.

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
| `tool_end` | `ToolEndPayload` (`call_run_id`, result, error, duration, telemetry) |
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
- one terminal hook event per workflow: `RunCompleted` for completion, failure,
  or cancellation, and `RunSuspended` for external input

The stream subscriber translates these into `workflow` stream events:

- **Non-terminal updates** (from `RunPhaseChanged`): `phase` only.
- **Terminal update** (from `RunCompleted` or `RunSuspended`): `status` +
  terminal `phase`, followed by `run_stream_end`.

`RunCompleted` carries `Labels`: the run-scoped labels provided when the
run started (`RunInput.Labels`), nil when the run had none. Completion
subscribers can attribute the terminal outcome (for example, call back into the
service that owns the run's source entity) without maintaining their own
runID-to-identity map. The same labels are exposed on `run.Snapshot.Labels` for
polling readers, replayed from the durable `RunStarted` record, so the identity
also remains available for suspended workflows and survives process restarts
on both engines. Labels merged by policy decisions
mid-run are not included; they remain observable via `PolicyDecision` events.

Terminal status mapping:

- `status="success"` → `phase="completed"`
- `status="failed"` → `phase="failed"`
- `status="canceled"` → `phase="canceled"`
- `status="suspended"` → `phase="suspended"`

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

`TimeBudget` counts active planner and tool work. Time between a suspension and
its continuation does not consume that budget. `FinalizerGrace` extends the Budget deadline into the Hard
deadline. A final planner activity that starts when Budget expires therefore
has at most that grace; earlier policy-triggered finalization may also use the
remaining Budget. Terminal event persistence runs afterward under its own
completion context. The default grace is 10 seconds; use
`runtime.WithRunFinalizerGrace` to override it for one run.

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

### Compression

Model-assisted summarization for long conversations. Compression separates the
trigger budget from the exact-retention budget:

- `CompressAtTurns` and `CompressAtMaxInputTokens` decide when summarization
  runs. The triggers are ORed.
- `KeepMaxTurns` and `KeepMaxInputTokens` decide which newest complete turns
  remain exact after summarization. When both are set, both limits apply.
- `KeepMaxInputTokens` never truncates a turn. The runtime walks backward from
  the newest turn and keeps only whole logical turns that fit the budget.
- Token counts are computed at runtime through `HistoryModel`, because provider
  tokenization depends on the deployed model. Token-budget compression requires
  a history model that implements `model.TokenCounter` with exact counts.
- One history-policy count includes the preserved system messages, candidate
  complete turns, and the currently advertised tools. It does not claim to
  include thinking or structured output that a planner may choose later.
- `CompressAtMaxInputTokens` is an exclusive trigger: a count equal to the
  threshold fits, while a larger count triggers compression. The runtime checks
  the newest turn even when retention uses only `KeepMaxTurns`, and rejects a
  generated summary that still leaves the history-policy request over the
  threshold.
- Bedrock uses its native Runtime `CountTokens` operation when the resolved
  model supports it. Claude Opus 4.7, Sonnet 5, and Mythos 5 require AWS's
  separate Mantle token-count endpoint, so this adapter returns
  `model.ErrTokenCountingUnsupported` for those models. Structured output is
  also unsupported for Bedrock counting because Runtime `CountTokens` cannot
  carry `OutputConfig`. Provider validation errors remain errors; the adapter
  never parses an error message into a fabricated count.

```go
// DSL
RunPolicy(func() {
    History(func() {
        CompressAtMaxInputTokens(120_000)
        KeepMaxInputTokens(40_000)
        KeepMaxTurns(12)
    })
})

// Registration
cfg := chat.ChatAgentConfig{
    Planner:      myPlanner,
    HistoryModel: smallModelClient, // Counts tokens and writes summaries.
}
```

The DSL values are generated defaults. Deployments can replace them when the
configured model has a different context window or operating budget:

```go
cfg := chat.ChatAgentConfig{
    Planner:      myPlanner,
    HistoryModel: smallModelClient,
    HistoryCompression: &runtime.HistoryCompressionConfig{
        CompressAtMaxInputTokens: 180_000,
        KeepMaxInputTokens:       60_000,
        KeepMaxTurns:             16,
    },
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

// Create a Gemini-on-Vertex client via runtime helper (Application Default Credentials)
geminiClient, err := rt.NewVertexGeminiModelClient(ctx, runtime.VertexConfig{
    ProjectID:      "my-gcp-project",
    Location:       "us-central1",
    DefaultModel:   "gemini-2.5-flash",
    HighModel:      "gemini-3-pro-preview",
    SmallModel:     "gemini-2.5-flash-lite",
    MaxTokens:      4096,
    ThinkingBudget: 10000,
})

// Create a Claude-on-Vertex client via runtime helper. This is pure
// construction: it builds an Anthropic SDK client against the SDK's Vertex
// transport and hands it to features/model/anthropic, which owns Messages
// translation and error classification for every Anthropic-hosted adapter.
claudeOnVertexClient, err := rt.NewVertexAnthropicModelClient(ctx, runtime.VertexConfig{
    ProjectID:    "my-gcp-project",
    Location:     "us-east5",
    DefaultModel: "claude-sonnet-4-5@20250929",
})
```

Runtime-owned model factories are transcript-stateless. Callers must pass the
complete provider-ready transcript in `model.Request.Messages`, and the runtime
persists canonical transcript deltas so they can be replayed from the durable
runlog when needed.

When `model.Request.Thinking.Enable` is true for a Bedrock adaptive Claude
model, the Bedrock adapter requests summarized reasoning display explicitly so
streamed `thinking` chunks stay visible. This includes Claude Sonnet 5, whose
always-on adaptive thinking otherwise defaults to a signature-only block with
no display text, as well as Claude Opus 4.7 and later adaptive revisions.

Gemini 3-class models attach an opaque thought signature to `functionCall`
parts (not just to thought parts). The `features/model/vertex` adapter
round-trips these through `model.ToolCall.ThoughtSignature` /
`model.ToolUsePart.ThoughtSignature` using the same base64 convention as
`ThinkingPart.Signature`; see "Tool-call thought signatures" above for how the
runtime captures and reattaches them without exposing the field to planners.

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
    80_000,            // Initial input tokens per minute
    1_000_000,         // Maximum input tokens per minute
)

limitedClient, err := rl.Middleware()(modelClient)
if err != nil {
    return err
}
if err := rt.RegisterModel("bedrock", limitedClient); err != nil {
    return err
}
```

The limiter reserves the provider's exact input-token count before each
request. It does not meter output-token quotas. For streams, it increases
capacity only after clean end-of-stream and reduces capacity when a terminal
stream error is rate limited.

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
    QueryRunCompletion(ctx, runID string) (*api.RunOutput, error)
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
    PublishRecord(call engine.RecordActivityCall) error
    ExecutePlannerActivity(call engine.PlannerActivityCall) (*api.PlanActivityOutput, error)
    ExecuteToolActivity(call engine.ToolActivityCall) (*api.ToolOutput, error)
    ExecuteToolActivityAsync(call engine.ToolActivityCall) (Future[*api.ToolOutput], error)
    NewTimer(ctx context.Context, d time.Duration) (Future[time.Time], error)
    Await(condition func() bool) error
    StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error)
    Detached() WorkflowContext
    WithCancel() (WorkflowContext, func())
    SetQueryHandler(name string, handler any) error
}
```

Custom engine adapters must implement the activity methods with the signatures
shown above. The activity methods and `Await` no longer accept a separate
`context.Context`; the `WorkflowContext` receiver owns cancellation for
scheduled work and deterministic waits. This is a source-breaking change for
custom adapters, which must remove the extra argument and use their
receiver-owned engine-native workflow scope.

`policy.CapsState.ExpiresAt` was also removed. Custom policy integrations must
stop constructing that field; the workflow loop now owns Budget and Hard
deadlines directly instead of encoding time in tool-call caps.

Custom adapters must apply `ActivityOptions.ScheduleToCloseTimeout` to the
complete planner activity lifetime, including queue time, retries, and retry
backoff. Return `engine.ErrPlannerActivityDeadlineExceeded` only when that
total planner deadline expires; attempt, queue, heartbeat, and parent
cancellation errors retain their original causes.

### Available Engines

**Temporal worker** — Production-grade durable execution:

Planner time budgets require Temporal Server 1.31 or newer so
Schedule-to-Close expiration carries server-owned timeout provenance.

```go
import temporal "goa.design/goa-ai/runtime/agent/engine/temporal"

eng, err := temporal.NewWorker(temporal.Options{
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
if err != nil {
    log.Fatal(err)
}
```

**Temporal client** — Start/query/cancel without local polling:

```go
eng, err := temporal.NewClient(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:  "temporal:7233",
        Namespace: "default",
    },
})
if err != nil {
    log.Fatal(err)
}
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

All callers implement the `mcp.Caller` interface. They return typed transport,
protocol, malformed-response, and tool-execution errors without retrying or
turning error text into control flow. Generated MCP executors classify those
errors into the canonical `planner.ToolFailure` contract.

### Server-initiated events (Broadcaster)

Generated MCP adapters can stream server-initiated events (notifications, resource updates) to multiple
subscribers via `mcp.Broadcaster`. The default in-memory implementation is:

```go
b := mcp.NewChannelBroadcaster(128, true) // (buf, drop)
sub, _ := b.Subscribe(ctx)
defer sub.Close()
```

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

The `runtime/agent/planner` package defines the canonical failed-tool contract.
Executors classify why execution failed separately from the legal next
transition.

```go
failure := &planner.ToolFailure{
    Kind:  planner.FailureInvalidCall,
    Error: planner.NewToolError("invalid tool arguments"),
    Recovery: planner.RecoveryDirective{
        Action:      planner.RecoveryCorrectCall,
        Issues:      validationErr.Issues(),
        PriorInput:  input,
        ExampleJSON: spec.ExamplePayload,
    },
}
```

### Validation Issues and Tool Failures

Tool calls can fail because the input payload is missing fields, violates constraints,
or has the wrong JSON shape. When that happens, callers generally need actionable,
field-level feedback rather than a generic failure string.

Goa‑AI supports two complementary paths that produce
`planner.FailureInvalidCall` with `planner.RecoveryCorrectCall`:

1. **Decode‑time validation (generated codecs)**  
   The generated tool codec validates the tool JSON payload before execution.
   If validation fails, the codec returns a generated validation error that exposes
   structured issues (`Issues() []*tools.FieldIssue`) and descriptions. The runtime
   converts these into `planner.ToolFailure` automatically. Generated codecs also
   turn JSON type mismatches into `invalid_field_type` issues with expected and
   actual JSON type metadata, so callers can produce guidance such as
   ``sections` must be a JSON array, not a JSON string`` without parsing schemas
   or error strings.

2. **Execution‑time validation (service / tool provider errors)**  
   When a tool provider calls a bound service method, the method may return a Goa
   validation error (for example `goa.MissingFieldError`, `goa.InvalidLengthError`, …).
   Providers should surface these as **structured validation issues** in the tool result
   message so consumers can build a `ToolFailure` without parsing error strings.

   - **Provider behavior (generated)**: generated providers call
     `toolregistry.ValidationIssues(err)` and, when issues are present, emit an error
     result that includes them.
   - **Wire protocol**: tool result errors may include `issues` (`[]FieldIssue`).
   - **Consumer behavior**: registry executors preserve `issues`, prior canonical
     input, and the generated tool example in a `correct_call` directive.

This keeps the contract strong and deterministic: validation stays at boundaries,
and “what to retry with” is computed from structured data, not heuristics.

---

## Model Middleware

The `features/model/middleware` package wraps opaque validated model clients.
Middleware receives a `model.Client` and returns `(model.Client, error)`;
always handle both values. The word **raw** is reserved for `model.Provider`
values and their unvalidated responses or streams.

External packages can no longer implement `model.Client`. Migrate a custom
client implementation into a provider, put custom wrappers below the validation
boundary with `model.WrapClient`, and expose only the opaque client returned by
`model.NewClient`:

```go
// Before: a custom wrapper implemented model.Client directly.
type loggingClient struct {
    next model.Client
}

// After: provider-side code implements model.Provider.
type loggingProvider struct {
    next model.Provider
}

func (p *loggingProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
    logRequest(req)
    return p.next.Complete(ctx, req)
}

func (p *loggingProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
    logRequest(req)
    return p.next.Stream(ctx, req)
}

provider := newCustomProvider()
modelClient, err := model.NewClient(provider)
if err != nil {
    return err
}
modelClient, err = model.WrapClient(modelClient, func(next model.Provider) model.Provider {
    return &loggingProvider{next: next}
})
if err != nil {
    return err
}
```

Code that deliberately calls a `model.Provider` directly may use
`model.NewRequestContract` to capture and apply the immutable output contract.
Normal planners, completions, and runtimes should receive an opaque
`model.Client` instead.

### Adaptive Rate Limiter

Apply adaptive rate limiting to handle provider throttling:

```go
import mdlmw "goa.design/goa-ai/features/model/middleware"

rl := mdlmw.NewAdaptiveRateLimiter(
    ctx,
    throughputMap,     // *rmap.Map for cluster-wide state (nil for local)
    "bedrock:sonnet",  // Model family key
    80_000,            // Initial input tokens per minute
    1_000_000,         // Maximum input tokens per minute
)

limitedClient, err := rl.Middleware()(modelClient)
if err != nil {
    return err
}
if err := rt.RegisterModel("bedrock", limitedClient); err != nil {
    return err
}
```

The rate limiter reserves the provider's exact input-token count and adjusts
that input-token budget from terminal provider outcomes. It probes upward after
a unary response or stream ends successfully and backs off after a unary or
streaming rate-limit error. It does not meter output-token quotas.

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
executor := runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
    // Access explicit metadata
    log.Printf("Executing %s in run %s, session %s", call.Name, meta.RunID, meta.SessionID)
    
    // Call your service
    result, err := myService.Execute(ctx, call.Payload)
    if err != nil {
        return nil, err
    }
    
    return runtime.Executed(&planner.ToolResult{
        Name:   call.Name,
        Result: result,
    }), nil
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
var ErrEmptyStream = errors.New("model: provider returned an empty stream")
```

`ErrEmptyStream` is joined (via `model.NewEmptyStreamError`) with a retryable
`unavailable` ProviderError when a provider terminates a stream before any
assistant message starts — the wire shape produced by intermittent empty model
completions. Detect it with `errors.Is(err, model.ErrEmptyStream)` and retry
the request a bounded number of times before surfacing the failure.

---

## Best Practices

1. **Register before running.** All agents and models must be registered before
   the first `Run` or `Start` call. Registration closes afterward.

2. **Use generated clients.** The typed `<agent>.NewClient(rt)` embeds route
   information and provides compile-time safety.

3. **Choose one streaming path.** Use `PlannerModelClient` for runtime-owned
   event emission, or use `ModelClient` with `planner.ConsumeStream` (or manual
   draining) when you want explicit control over the validated stream.

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
| **Turn** | One user message or submitted external-input response and the agent work performed by one run. |
| **Planner** | Decision-maker that analyzes messages and returns tool calls or final responses. |
| **Toolset** | Collection of related tools with shared execution logic. |
| **Tool Spec** | Metadata and JSON codecs for a tool (name, schema, codec functions). |
| **Bounds** | Metadata describing how a tool result was truncated or limited. |
| **Hook** | Internal event emitted for observability (memory, streaming, telemetry). |
| **Stream Event** | Client-facing event delivered via Sink (tool progress, assistant replies). |
| **Finalizer** | Aggregates child results into parent tool result for agent-as-tool (does not propagate artifacts). |
| **Reminder** | Structured backstage guidance injected into planner prompts. |
