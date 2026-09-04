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
    "time"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/runtime"
    storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

func main() {
    // 1. Create the local store, session, and runtime.
    ctx := context.Background()
    runtimeStore := storageinmem.New()
    if _, err := runtimeStore.CreateSession(ctx, "session-1", time.Now().UTC()); err != nil {
        panic(err)
    }
    rt := runtime.New(runtimeStore)

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
    }}, runtime.WithRunID("run-1"))
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
        WorkerOptions: temporal.WorkerOptions{TaskQueue: "assistant"},
    })
    if err != nil {
        log.Fatal(err)
    }
    defer func() {
        if err := temporalEng.Close(); err != nil {
            log.Printf("close Temporal engine: %v", err)
        }
    }()

    // MongoDB memory store for persistent transcript memory.
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
        runtimeStore, // Host-owned durable runtime storage
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

Every low-level `model.StructuredOutput` must provide a nonempty `Name` and a
nonempty, compilable JSON Schema. The shared request boundary compiles that
schema once for the private request copy passed through a normal `model.Client`
call. The schema remains raw JSON bytes until the JSON Schema compiler reads it
through its in-memory resource loader; core runtime APIs never expose a
map-shaped schema contract. A raw provider called directly, including through
`CountTokens`, compiles its own request contract before translation. The
validated client checks unary JSON immediately. For streaming, it retains the
final `completion` chunk, drains the provider, validates the complete response,
and requires the exact completion bytes to match before returning the retained
chunk. A generated or caller-supplied completion validator adds checks but
cannot replace schema validation or byte reconciliation. Provider enforcement
remains an earlier optimization. Generated completion helpers derive the name
and schema from the validated completion DSL.

Unary completion makes exactly one model call. The response is decoded with the
generated codec. If its JSON violates the completion contract, the helper
returns a non-retryable `planner.OutputContractError` and does not ask the model
again. The same error is returned when the provider reports that generation
stopped at its output limit, even if the partial JSON satisfies the schema.
Provider errors and malformed response envelopes are also returned immediately.
On success, `completion.Response.ModelResponse` contains the exact model
response and its token usage. On failure, the response is nil, matching the
`model.Client` contract.

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
- The low-level validated stream holds the final completion and every later
  chunk until the provider ends normally, both final representations satisfy
  the request schema, and their JSON bytes match exactly. Typed helpers then
  decode that accepted value. They never restart after exposing previews; an
  invalid stream returns the same non-retryable
  output-contract error without exposing the final value.
- If a streaming provider reports that generation stopped at its output limit,
  previews already returned remain provisional. The final completion is not
  returned, and `Value` remains unavailable.
- `Value` becomes available only after `Recv` reaches and validates the final
  completion; there is no separate decoder that can accept an unchecked chunk.
- Completion streams use their generated typed wrapper directly; do not route
  them through planner streaming helpers, which are for assistant transcript
  text and tool execution events.
- Providers that do not implement structured output surface `model.ErrStructuredOutputUnsupported`.
- Generated schemas are the provider-neutral source of truth. The Gemini
  adapter translates `oneOf` choices to `anyOf` and omits only validation and
  annotation keywords Vertex rejects. The validated client then applies the
  complete original schema to returned tool calls. Unknown structural keywords
  fail before the request is sent; other provider adapters must likewise fail
  explicitly when they cannot preserve the declared contract.

### Unadvertised tool name recovery

Anthropic, Bedrock, OpenAI, and Gemini/Vertex compare every returned tool name
with the exact tools advertised for that model request. If a response names a
different tool, the adapter rejects the complete response. Text, arguments,
call identifiers, and earlier valid tool blocks from that response do not enter
the accepted provider transcript and no tool from the response runs. Assistant
text already delivered is committed separately as a plain assistant message.
After detecting the name, a streaming adapter stops semantic output and drains
to normal provider completion so later cumulative token usage is still counted.
Cancellation, deadline, transport, or incomplete-stream failures remain those
failures instead of becoming recoverable name errors.

Bedrock keeps two name maps for different jobs. The map used to accept a
response contains only tools advertised in the current request. A temporary
copy also contains tool names from transcript history so Bedrock can receive
earlier `tool_use` messages in its required name format. Historical names never
enter response admission. If an advertised and historical name, or two
historical names, produce the same Bedrock identifier, the request fails before
Bedrock is called.

The runtime retains only the untouched returned name and the provider's token
usage. Generic error text is fixed and does not render the name, rejected
payload, schema, validator message, response, or correction guidance. Framework
code that needs the rejected identity uses `model.UnadvertisedToolName`; code
that needs evidence, usage, or correction uses the corresponding typed
`OutputValidationError` accessors. Bedrock may remove its `$FUNCTIONS.` prefix
from a separate copy used for lookup, but recovery keeps the prefixed name. The
next planner activity starts from the last accepted conversation, receives the
tools available for that new request, and gets a fixed reminder to choose one
of those exact names. The runtime does not guess a replacement, apply aliases
or fuzzy matching, copy the catalog into recovery state, or change the
available tools.

For streams, validation can report the rejected name and valid token usage as
soon as `Recv` detects the bad output. The runtime does not commit recovery at
that point. The validated client keeps provider receive and close results,
validation, incomplete consumption, each observer result, every prepared
`Finish` result, usage recording, staging, and cancellation in separate private
slots. Every receive or close observer gets its own safe copy of the same
provider result. One observer's error never becomes the next observer's input.
Every prepared `Finish` callback likewise receives the same result collected
before any finisher ran. The runtime sees the frozen record only after all
finishers return.

`ValidatedStream.Close` keeps its cleanup-only contract for callers that handle
receive and close results separately. Code that owns the complete operation
passes its exact receive or processing result to `ValidatedStream.Finalize`.
Finalization returns that exact validation when provider cleanup is the only
additional failure, while preserving observer, lifecycle, context, incomplete
stream, and other independent failures. Both methods share the same
exactly-once close work, and `Close` always returns its cleanup result even when
`Finalize` has already determined the complete operation result. `Recv`,
`Close`, and `Finalize` are serialized. An observer callback must not call any
of those methods because each waits for the callback to finish.

The first `Finalize` call owns the operation result. Repeating it with the same
error returns that cached result; a concurrent or later call with a different
error reports misuse without replacing the first result. Passing nil is valid
only after clean EOF. Passing nil before EOF closes the stream and returns the
incomplete-stream failure.

Only the literal `io.EOF` value from the provider marks normal stream
completion. A wrapped EOF is a provider failure, and EOF returned by an
observer is an observer failure. Recovery never searches a joined error for
EOF.

After the planner returns, the activity closes unfinished calls, waits for
their frozen records, and makes one decision. Live text already sent remains
append-only. Before recovery or terminal failure continues, the workflow saves
that exact text as a plain assistant message under the response ID used by its
fragments. Rejected tool calls and other provider-only response parts do not
enter the transcript. The activity commits recovery only when an exact staged validation belongs to
the same model call and every operation that can change that validation
decision is clean. If provider cleanup also fails after validation completed,
the validation remains the operation result and stream tracing records the
cleanup failure separately. An observer, finisher, usage-recording, staging,
ordinary provider-read, or premature-close failure keeps its own failure
category instead of producing `ModelInvocationRecovery`. If text was already
delivered, the activity returns that text with the standardized failure so the
workflow can save the text before ending the run. If no text was delivered, the
original activity error remains an activity error; Plan and Resume still make
only one attempt. Cancellation and deadline errors remain activity errors and
do not create a durable partial response. Planner code cannot broaden recovery by ignoring,
joining, or replacing the validation error returned by `Recv`.

Each retry consumes one existing `MaxRecoveryTurns` entry. Repeated misses end
through the normal recovery-cap path. Cancellation, deadlines, transport
failures, malformed output that has no attributable terminal usage, and
complete-answer corrections remain on their existing paths. A completed tool
call whose arguments are not valid JSON uses fixed replacement guidance; its
raw bytes, provider diagnostics, and tool identity do not enter workflow state.
Provider usage from each rejected invocation is counted once.

Temporal records the optional name in `ModelInvocationRecovery` on the planner
activity result and its next input. Histories recorded before this field existed
remain readable because the field is absent. Deploy the runtime and Temporal
workers together. After a history records an unadvertised-name recovery,
rolling that history back to an older worker is unsafe because the older worker
does not understand the new field. This change requires no Goa regeneration,
client regeneration, or public wire-client update.

The v0.78.5 error-privacy and stream-finalization correction does not change an
activity-result, checkpoint, schema, generated-type, or wire shape. Existing
v0.78.4 records therefore remain readable by v0.78.5, and rollback to v0.78.4
is structurally safe. The reason-fingerprint input does change: v0.78.5 hashes
only the private validation-cause text, while v0.78.4 hashed the complete
diagnostic error text. Mixed v0.78.4 and v0.78.5 workers can therefore emit
different reason fingerprint values for the same failure, depending on which
worker executes the activity. The value is opaque evidence, so either version
can load and publish either fingerprint. Rolling back makes new failures use
the v0.78.4 value again and also restores model-authored values in generic
error text and the lost-recovery behavior when validation and provider cleanup
fail together. The older-worker restriction in the preceding paragraph applies
only to runtime versions from before `ModelInvocationRecovery` existed.

### Tool input validation across model gateways

A raw model gateway transports provider chunks and the complete response; it
does not replace the request owner's tool input contract. The consuming process
keeps the exact advertised schema and any attached generated decoder together.
Provider adapters still reject malformed JSON, unknown tool names, invalid
identifiers, illegal event order, incomplete streams, and provider errors
before output crosses the transport. For malformed tool argument JSON, an
adapter may finish reading only terminal completion and usage events, then
return fixed replacement guidance through the existing typed output rejection.
It never exposes or repairs the malformed bytes.

The consuming `model.Client` retains streamed tool argument fragments and
completed calls, drains the provider through terminal usage and response, and
first reconciles every chunk with that response. It then applies each tool's
advertised schema before its generated payload decoder. The correction and
terminal-error rules are the same as
[Model-Visible Tool Arguments](#model-visible-tool-arguments). The client
exposes no argument fragment or completed call from a rejected response. A
stream mismatch or provider-protocol failure remains terminal and takes
precedence over argument correction.

Provider processes that need adaptive token admission can apply
`AdaptiveRateLimiter.WrapProvider` while preserving this raw gateway contract.
Planner and runtime processes use `AdaptiveRateLimiter.Middleware` when the
limiter belongs beneath a validated client.

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

This adapter boundary lets an inference backend change providers without
changing its planners or runtime flow.

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
Sonnet, Opus, and Haiku 4.5 or later. On Bedrock, Claude 4.6 uses one private
forced tool with `strict: true`, so Converse and Runtime `CountTokens` receive
the same provider-enforced schema. Claude 4.5 retains native `OutputConfig`
because its manual thinking mode cannot use forced tools. Newer Claude models
for which Bedrock exposes forced tools but not `OutputConfig` or `strict` tools
use one private non-strict tool. This fallback keeps those models available for
typed completions, but Bedrock does not enforce the schema: the adapter unwraps
the private tool value, and the validated `model.Client` rejects malformed or
schema-invalid JSON as `model.OutputValidationError` before the caller can
observe a response. Generated typed-completion decoders then apply the exact Goa
result contract at the same boundary.
`bedrock.NewAnthropic` makes the same selection before Anthropic Messages
encoding. Sonnet 5 and Opus 5 use the private non-strict tool because Bedrock
Messages rejects `output_config.format` and every `strict` property. Unary and
streaming responses are converted back into canonical completions, and Mantle
counts the same tool definition and forced choice sent to `InvokeModel`.

Bedrock may omit tool-input delta events when a model selects a tool without
supplying any arguments. The streaming adapter emits the canonical `{}` payload
only when the tool's exact generated or transported validator accepts `{}`.
Tools with required fields still fail output validation, and supplied optional
arguments remain model-authored input. `ToolDefinition.NoArguments` is
stronger: it means tool selection is the complete model decision, so the
adapter discards any provider-authored argument text and always emits `{}`.

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

Pass the required runtime store to `runtime.New`, followed by functional
options:

```go
rt := runtime.New(
    runtimeStore,                         // Runs, checkpoints, and ordered records
    runtime.WithEngine(engine),          // Workflow backend (required for production)
    runtime.WithMemoryStore(store),      // Transcript persistence
    runtime.WithPromptStore(promptStore),// Scoped prompt overrides
    runtime.WithStream(sink),            // Real-time event streaming
    runtime.WithPolicy(engine),          // Policy enforcement
    runtime.WithHooks(bus),              // Custom event bus (rare)
    runtime.WithLogger(logger),          // Structured logging
    runtime.WithMetrics(metrics),        // Counter/histogram recording
    runtime.WithTracer(tracer),          // Distributed tracing
)
```

The runtime store has no default. When options are omitted, the runtime uses
these defaults:

| Option | Default |
|--------|---------|
| Engine | In-memory (synchronous, non-durable) |
| MemoryStore | None (transcripts not persisted) |
| PromptStore | None (baseline prompt specs only, no scoped overrides) |
| Stream | None (no external event delivery) |
| Policy | None (all tools allowed, caps from agent registration) |
| Hooks | In-process bus |
| Logger/Metrics/Tracer | No-op implementations |

Each generated `AgentDefinition` contains the agent's workflow name and default
task queue. Callers and workers use that same definition. A caller may still
select a different queue for one run with `runtime.WithTaskQueue(...)`.
Semantic planner and tool attempt budgets come from the DSL
(`RunPolicy.Timing`) or per-run overrides (`runtime.WithTiming(...)`). Configure
Temporal queue-wait and liveness behavior on
`temporal.Options.ActivityDefaults` when constructing the engine.

### Prompt Registry and Overrides

The runtime always initializes `Runtime.PromptRegistry`. Prompt management has two layers:

- **Baseline specs**: register immutable `prompt.PromptSpec` definitions in memory.
- **Scoped overrides**: optionally resolve workspace/session overrides through `prompt.Store`
  (`runtime.WithPromptStore(...)`).

```go
import (
    promptmongo "goa.design/goa-ai/features/prompt/mongo"
    clientmongo "goa.design/goa-ai/features/prompt/mongo/clients/mongo"
    "goa.design/goa-ai/runtime/agent/prompt"
)

mongoClient, _ := clientmongo.New(clientmongo.Options{
    Client:     rawMongoClient,
    Database:   "assistant_runtime",
    Collection: "prompt_overrides",
})
promptStore, _ := promptmongo.NewStore(mongoClient)

rt := runtime.New(
    runtimeStore,
    runtime.WithPromptStore(promptStore),
)

_ = rt.PromptRegistry.Register(prompt.PromptSpec{
    ID:       "assistant.system",
    AgentID:  "assistant",
    Role:     prompt.PromptRoleSystem,
    Template: "You are {{ .AssistantName }}.",
})
```

Rendering returns text and a versioned `PromptRef`; it never writes runtime
storage. When a `prompt.RenderRecorder` is present in the context, every
successful render also records one `prompt.RenderEvent` containing the resolved
prompt ID, version, and scope. Failed renders record nothing.
`RenderRecorder.Events` returns completed renders in stable prompt ID, version,
session, and scope order. Concurrent completion order therefore cannot change
the exact workflow start request.

Planners normally call `PlannerContext.RenderPrompt(...)`. The runtime supplies
the recorder, returns its events with the accepted planner activity result, and
then stores them as `PromptRendered` records from workflow history. Copy the
returned `PromptRef` into the model request as described under [Model
Clients](#model-clients).

Callers that render text before `Start` must carry the recorded events with the
same run input that contains that text:

```go
recorder := prompt.NewRenderRecorder()
renderCtx := prompt.WithRenderRecorder(ctx, recorder)
content, err := rt.PromptRegistry.Render(renderCtx, "assistant.system", scope, data)
if err != nil {
    return err
}

messages := []*model.Message{{
    Role:  model.ConversationRoleSystem,
    Parts: []model.Part{model.TextPart{Text: content.Text}},
}}
_, err = client.Start(ctx, sessionID, messages,
    runtime.WithRunID(runID),
    runtime.WithRenderedPrompts(recorder.Events()),
)
```

`WithRenderedPrompts` is for initial input whose rendered text is already in
`Messages`; continuations restore their own checkpoint state and reject it. The
runtime also rejects an event without a prompt ID or version, or whose scoped
session differs from the run session. The
accepted root or child workflow stores `RunStarted` and then these prompt events
before planner work. If session ending prevents the run from starting, it stores
the canceled start and no prompt events. Consumer-side agent-tool rendering runs
in an activity and carries its recorded text and events in the child input.
Workflow replay therefore reuses the activity result instead of reading prompt
storage again. `RunOneShot` gives
its callback a recorder context and stores the same events on the one-shot run.
Only the path into the accepted run differs; the event shape and durable
`PromptRendered` record are the same.

### Two Deployment Patterns

**Worker process** — Registers agents and executes workflows:

```go
rt := runtime.New(runtimeStore, runtime.WithEngine(temporalWorker))

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
rt := runtime.New(runtimeStore, runtime.WithEngine(temporalClient))

// No registration needed; use generated client with route info
client := chat.NewClient(rt)
out, err := client.Run(ctx, "session-1", msgs, runtime.WithRunID("run-1"))
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

1. **Start** — `client.Run()` or `client.Start()` submits a stable run ID and
   exact request to the engine
2. **PlanStart** — Planner receives messages and decides: answer or call tools?
3. **Execute** — Tools run as activities (parallel by default)
4. **PlanResume** — Planner receives tool results and decides next step
5. **Repeat** — Loop continues until planner returns a `FinalResponse`,
   `FinalToolResult`, or a successful `TerminalRun` tool completes the run

### Workflow Contracts

- **SessionID is required.** `Start` fails fast if `SessionID` is empty.
- **RunID is required for sessionful work.** Supply `WithRunID`; the runtime
  creates an ID only for sessionless one-shot work.
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
follow the required rules, it chooses one of three explicit outcomes:

- `planner.NewOutputContractError(violation)` ends planning without another
  model turn.
- `planner.NewRecoverableModelAnswerError(violation, answer, correction)` uses
  one recovery turn to replace the exact rejected final answer. Ordinary tools
  are not advertised on that turn.
- `planner.NewRecoverableModelPlanningError(violation, response, correction)`
  uses one recovery turn to replace output from the exact rejected tool-capable
  planning response. The response may have omitted a required call. The current
  executable tool catalog remains advertised.

Both recoverable constructors require the exact message returned by the model
client and bounded correction guidance. The workflow records its fingerprint
and usage but does not put the rejected body in durable recovery state. Do not
use these errors for model-provider failures, timeouts, canceled work, or
network errors; those keep their existing retry behavior.

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
rehydrates `Payload`, `Result`, `ServerData`, `ModelToolCallID`, and
planner-visible result metadata from the canonical run log inside
`PlanResumeActivity` before invoking the planner. `ToolCallID` identifies the
runtime execution. `ModelToolCallID` identifies the exact provider transcript
tool-use part that produced the call and is empty for planner-authored calls.
Planners must not use `ModelToolCallID` for execution, retry, or persistence
correlation. A result recorded in the same run must follow its scheduled call.
When supplied external input starts a continuation run, that run may contain the
result without the earlier schedule; hydration loads the schedule from
`CallRunID` and requires the call ID, tool name, and parent call to match exactly.

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
  for each of the time, tool-call, and recovery-turn limits; before
  the first planner activity, the runtime validates the complete set against
  the agent's registered generated codecs and rejects tools that require
  confirmation,
- when one of those limits is reached, the workflow selects the matching call
  without loading saved messages, adds current run identifiers and labels,
  and executes the call through the existing terminal-tool path,
- when a model returns malformed tool argument JSON, fails an advertised input
  schema, or receives a non-nil `*tools.ValidationError` from its input decoder,
  the model boundary produces fixed replacement guidance; that guidance never
  copies tool names, submitted values, schema details, raw provider output, or
  provider diagnostics,
- the workflow ties that guidance to the exact rejected invocation selected in
  invocation start order, records its token usage, keeps the malformed call out
  of transcript history, and schedules one normal resume activity with the
  executable tool catalog still available; ordinary decoder and internal
  errors remain terminal,
- Temporal histories written before this behavior replay unchanged. Once a
  history contains a `ModelInvocationRecovery` planner activity result, every
  worker that may process that history must run a runtime version that
  understands this result. Do not mix older and newer workers on those
  histories, and do not roll them back to an older worker,
- a planner may return `NewRecoverableModelAnswerError` when it rejects a
  completed final answer and can state how the model should replace it; the
  workflow records the rejection and token usage, then schedules a
  synthesis-only resume activity with that guidance,
- a planner may return `NewRecoverableModelPlanningError` when it rejects
  completed output from a tool-capable planning turn and can state how the
  model should replace it; the workflow records the same evidence, then
  schedules a normal resume with the current executable tool catalog,
- model-output recovery histories now require the explicit `answer` or
  `planning` kind. Histories created by an older runtime while such a recovery
  was pending cannot resume on this version. New histories require matching
  workers and cannot be rolled back to an older runtime,
- `MaxRecoveryTurns` counts replacement planner activities scheduled after
  rejected tool output, a rejected model invocation, or a rejected completed
  model response; successful budgeted tool work does not reset this count;
  agents that omit the setting receive three turns; the terminal finalization
  activity that explains or records exhaustion is not a replacement attempt
  and does not consume this budget,
- when the run omitted `LimitTerminalPlans`, or a tool failure requires
  finalization, `PlanResumeInput.Finalize` is non-nil and the planner may close
  through terminal bookkeeping tools instead of prose; the runtime admits only
  `TerminalRun()` calls (`TerminalRun()` implies bookkeeping), executes them
  inside the remaining hard-deadline window, stamps generated tool-call IDs
  with an opaque SHA-256 digest of length-delimited run ID, turn ID, attempt,
  batch index, and exact tool name, and requires every terminal side effect in
  the batch to complete successfully; a rejected finalizer output or terminal
  tool failure marked `correct_call` spends the existing recovery-turn budget
  while retaining finalization, and only the exact failed terminal tool is
  advertised for a tool-call repair,
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

Consumers of fixed-limit and planner-authored finalization calls, including
`tool_failure`, read the termination reason from
`runtime.FinalizationReasonLabel` (`goa-ai.finalization_reason`).

This release renames the old failed-tool setting because the same limit now
applies to replacement answers. Replace
`MaxConsecutiveFailedToolCalls(n)` with `MaxRecoveryTurns(n)` in the DSL and
replace `WithRunMaxConsecutiveFailedToolCalls(n)` with
`WithRunMaxRecoveryTurns(n)` in runtime callers, then regenerate agent code.

The same source-level rename applies to code that builds policy values
directly:

- `runtime.RunPolicy.MaxConsecutiveFailedToolCalls` and
  `runtime.PolicyOverrides.MaxConsecutiveFailedToolCalls` become
  `MaxRecoveryTurns`.
- `api.LimitTerminalPlans.FailedToolCallCap` and
  `runtime.LimitTerminalPlans.FailedToolCallCap` become `RecoveryCap`.
- `policy.CapsState.MaxConsecutiveFailedToolCalls` and
  `RemainingConsecutiveFailedToolCalls` become `MaxRecoveryTurns` and
  `RemainingRecoveryTurns`.
- `planner.TerminationReasonFailureCap` becomes
  `TerminationReasonRecoveryCap`.
- Generator integrations that inspect evaluated or rendered policy data must
  use `expr.CapsExpr.MaxRecoveryTurns` and
  `codegen.CapsData.MaxRecoveryTurns`.

These names and their serialized field names are intentionally breaking.
Suspensions written by this runtime use `goa-ai.run-suspension.v7`. Earlier
versions cannot resume. Version seven derives the exact ordered correction
catalog from saved typed failures instead of storing a duplicate catalog. It
also requires complete successful tool results: a result-bearing tool stores
JSON accepted by its generated codec, while a successful tool without a result
type and a failed tool store no result JSON. Each model-authored await item
stores its runtime `ToolCallID` separately from the provider
`ModelToolCallID`.

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
- correctable failures return to the planner while the normal tool-call and
  recovery-turn budgets permit another attempt,
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
    Await         *Await           // Request clarification or external tool results
    ExpectedChildren int           // Optional hint for nested child results
    Notes         []PlannerAnnotation
}

type ToolOutput struct {
    Name            tools.Ident
    ToolCallID      string
    ModelToolCallID string          // Provider transcript ID; empty for planner-authored calls
    Payload         rawjson.Message
    Result          rawjson.Message
    Failure         *ToolFailure    // Mutually exclusive with Result
}
```

These fields answer different questions:

| Contract | Scope | Question answered |
| --- | --- | --- |
| `ToolSpec.Tags` | One tool for every run | Which flat labels are available to generic policy and UI filtering? |
| `ToolSpec.Meta` | One tool for every run | Which inert generated annotations are available to their named consumers? Metadata alone changes no runtime behavior. |
| `ToolSpec.Bookkeeping` | One tool for every run | Does this call consume the tool-call budget, and does its success independently schedule another planner turn? |
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
the failed tool available and attaches correction evidence without
requiring a retry. Its `PriorInput` is a clone of the exact model-authored
payload; execution keeps the separately compiled or injected `Payload`.
Executors and tool activities return the failure classification, error,
recovery action, and generated field issues without constructing correction
evidence. Before recording the failure, the workflow requires the retained
provider tool-call ID and sets `PriorInput` and `ExampleJSON` from the original
call and registered tool specification. Runtime-authored calls have no provider
tool-call ID, so a `correct_call` result for an automatic continuation is a
contract error and its private execution payload is never shown to the model.
The planner may combine work, call any advertised tool, await input, or answer.
Historical tool calls retain deterministic provider-name projection
independently of the current catalog. `replan` removes the failed tool while
permitting another advertised action, input request, or answer;
`finish` forbids more tools. When the same tool has both correction and replan
failures in one batch, the correctable failure keeps that tool available. A
recovery turn may end with an input suspension; continuing retains the selected
failure evidence. A failed batch clears its earlier synthesis intent; a new
retry batch may request `SynthesizeAfterTools` again.

Saved work must match the current generated `AgentDefinition`. Removing a tool,
changing an incompatible codec, or changing completion policy deliberately
invalidates a suspension that depends on the old contract. Continuation
preparation rejects it before the workflow engine receives the successor run
ID. Goa-AI does not keep a retired tool catalog or use an older worker
registration as a fallback.

`RegisterToolset` validates agent tools before adding any part of the toolset to
the runtime. A registration with agent-tool execution configuration requires
every specification in that toolset to be marked as an agent tool; a marked
specification requires agent-tool execution configuration. Mixed marked and
unmarked specifications are invalid. Each generated agent-tool specification
must name the same agent as the registration's generated `AgentDefinition`.
That definition is the only owner of the child agent ID, workflow name, default
task queue, tool contracts, and required labels. Missing or conflicting values
fail application setup; the runtime never infers the marker or falls back to
ordinary tool activity execution. Generated registrations already satisfy this
contract.
Applications that construct `ToolsetRegistration` directly must update partial
agent-tool registrations before upgrading.

Every accepted `correct_call` recovery reloads each failed output by its saved
call ID, reads the typed recovery action, and derives tool names in
first-failure order while removing later copies of the same name. It requires
each name in the current executable toolset registration and advertises only
that registration's exact definitions for the correction turn. An unknown
tool, a specification without its executable toolset, or a tool the current
registration no longer contains fails before a model call.

Run tag restrictions still filter the correction catalog before the planner
runs. Runtime policy and authorization implemented by the tool's downstream
executor still evaluate the corrected call. Removing the current registration
revokes recovery immediately.

This recovery does not add an activity, change the resume activity name, or
change workflow command ordering. A turn with no `correct_call` failure
continues to advertise only the agent's current `Specs`, and the first normal
turn after correction returns to those current tools.

Suspensions omit `PendingRecoveryCatalog` only when typed pending failures
contain `correct_call`. The current reader derives the exact
catalog from those failures and rejects a serialized duplicate. Other recovery
actions still require their serialized catalog because the failed outputs do
not determine every tool that the planner may use.

Applications should update workers for every workflow and activity queue
together before accepting new work. This coordinated update keeps one runtime
contract across the application. Saved suspensions must use the current schema;
older suspension versions are rejected. Before upgrading across a suspension
schema change, finish or explicitly end incompatible suspended runs.

The workflow selects current recovery outputs by stable call ID in
`PlanActivityInput`. Empty recovery IDs are omitted from canonical JSON.
Workflow workers and generated callers must use the same input shape. A future
shape change requires a coordinated release that retires incompatible ongoing
work and saved suspensions before the new workers start.

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
- during forced finalization, a terminal bookkeeping call marked `correct_call`
  may use the bounded recovery-turn budget to replace that exact call; the
  finalization reason remains present and every other terminal failure ends
  finalization,
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

Requests copied from a model response keep their provider correlation ID and
must name a tool in that advertised list. Requests constructed by planner code
leave the correlation ID empty and must name a tool in the agent's generated
executable list. Hand-built agent definitions must populate this list; an empty
list means the agent has no executable tools. During exact call correction, the
list contains only the saved tool contracts being repaired. Dedicated
continuation tools remain absent from
the advertised list; trusted planner code may call them with a typed cursor
payload when the agent's generated definition includes them. A direct call
remains standalone and does not create a model-facing continuation action or
expose its next cursor in a reminder.

`ToolDefinition.NoArguments` means choosing the tool is the complete model
decision. The Bedrock streaming adapter still counts provider-emitted argument
text against response bounds, but exposes the canonical payload `{}` rather
than retaining text the runtime does not use. The runtime sets this only for
generated continuation actions whose execution payload is already derived from
saved query state. Request validation checks `{}` against this model-facing
contract; the generated execution decoder validates the separate payload after
the runtime adds the saved cursor.

Generated tool definitions also carry precomputed provider projections. The
DSL-authored top-level Goa `Example(...)` on a payload becomes the only
top-level provider example: providers that consume schema annotations use the
generated schema `example`; direct Anthropic and Claude-on-Vertex use the
generated schema without the root example plus top-level `input_examples`;
Claude through `bedrock.NewAnthropic` retains the schema annotation because
Bedrock Messages rejects `input_examples`.
Runtime code does not parse or rewrite schemas to discover examples. Boundaries
that transport model tools between processes should use `model.ToolInputContract`
so the complete provider-neutral input contract stays intact until the provider
adapter chooses a projection.

Remote model transports must preserve model-output rejection as
`model.OutputValidationError`, separately from provider and internal failures.
The transport must first decode a wire-level output-rejection variant rather
than infer that classification from an arbitrary error. The `cause` passed to
restoration is the error decoded from that output-validation variant; it is not
an independent provider or internal error transmitted alongside the variant.
The variant's restoration metadata is limited to a closed
`OutputValidationKind`, `ResponseEvidence`, validated `TokenUsage`, and, when
advertised input validation already produced it, a separate fixed
`RecoveryCorrection`; it never carries the rejected response body. The kind
contains no response text, provider text, tool names, arguments, identifiers,
or schema paths. Do not derive correction guidance from the kind, rejected
output, cause text, or a schema after transport.

After decoding that variant, call
`model.RestoreOutputValidationError(kind, cause, evidence, usage)`. It validates
the closed kind, cause, evidence, and usage and returns a terminal error with an
empty correction. Empty or unrecognized kinds, provider failures,
unsupported-capability and token-counting sentinels, cancellation, deadlines,
and nested `OutputValidationError` values contradict the decoded variant and
are rejected.
If the wire variant also carried correction guidance, pass the returned
terminal error to
`model.RestoreCorrectableOutputValidationError(restored, correction)`. The
second function accepts only an error produced by the first function, requires
a nonblank, valid UTF-8 correction of at most 4,096 bytes, and preserves
accepted bytes exactly.

Neither function reconstructs or exposes the rejected response. The correction
applies only to that rejected invocation and its immediate replacement planner
turn. The workflow remains responsible for scheduling the replacement and
enforcing `MaxRecoveryTurns`; provider failures, stream failures, and output
failures without fixed replacement guidance remain terminal.

This restoration signature is an API break for custom model transports. Upgrade
the transport producer and consumer together, regenerate application code
against the upgraded framework, and deploy the new binaries as one compatible
set. Built-in gateway validation remains local to each process, so this change
adds no gateway wire field. Runtime activity and hook records add the kind as
an optional JSON field: histories written before the field existed replay with
it absent, as do current planner-authored policy rejections. New mechanical
rejections always write a valid kind. Do not roll a workflow back to a binary
that predates the field after the new binary has written categorized activity
results or hook records; remove legacy-history coverage only after every
retained workflow created by the older binary has expired.

### PlannerEvents

`PlannerEvents` lets planner code publish semantic progress and usage that the
planner produces itself. Model-client text and thinking bypass this interface:
the runtime sends each validated fragment from the designated planner call
directly to the session stream. Partial tool arguments remain inside model
validation until the complete call is available.

```go
type PlannerEvents interface {
    PlannerThought(ctx context.Context, note string, labels map[string]string)
    UsageDelta(ctx context.Context, usage model.TokenUsage)
}
```

---

## Model-Visible Tool Arguments

Every tool input shown to a model is a JSON object. `Args` may define an inline
object or an object-shaped user type. Primitive, array, map, and `OneOf` roots
are rejected when the design is evaluated. Primitive, array, map, and `OneOf`
fields remain valid inside that object. Omitting `Args` on an unbound tool defines the empty
object `{}`, never `null`. Omitting `Args` on a tool with `BindTo` uses the bound
method payload, which must satisfy the same object-shape rule. This restriction
does not apply to `Return`; tool results may use any Goa type.

Generated input schemas and codecs agree on nested object behavior:

- Generated objects reject properties that the design did not declare.
- Maps remain open and accept keys supplied at runtime.
- A `OneOf` value accepts only `type` and `value`. Both properties are required
  and non-null. `type` must name a declared branch, and `value` must satisfy
  that branch's complete nested contract.

The validated model client checks each complete tool call before planner code
can observe it. It first applies the exact JSON Schema advertised with that
tool, then calls any input decoder attached to the same definition. A
schema rejection is eligible for a replacement turn within the configured
recovery limit. A decoder rejection is eligible only when it returns a non-nil
`*tools.ValidationError`,
the typed error generated decoders use for invalid model-authored fields.
Correction text is fixed and never includes tool names, schema details, or
rejected argument values. An ordinary decoder or internal error is terminal. If an error combines
several causes, the call is correctable only when every cause is an
advertised-schema rejection or a non-nil `*tools.ValidationError`; one internal
cause makes the combined error terminal. The runtime still enforces its
configured recovery-turn limit.

---

## Streaming Planners

When using model streaming, planners now have two explicit integration styles.
Choose one per planner call.

### Option 1: PlannerModelClient (Recommended)

`PlannerContext.PlannerModelClient(id)` returns a planner-scoped client that
sends validated assistant text and thinking to the session stream while its
`Stream(...)` method drains the provider response. It returns a
`planner.StreamSummary` containing accumulated text and complete validated tool
calls. Each text fragment is append-only once sent. For an accepted response,
the workflow appends the complete provider transcript and emits
`assistant_turn` with the same response ID and exact ordered messages. Consumers
derive display text from those messages and retain their structured parts and
metadata. For a
rejected response or ordinary failure reported before activity cancellation, it
first appends the exact text already sent as a plain assistant message and emits
the same committed event, then continues recovery or ends the run. Usage
includes every attempted invocation. A planner client is single-use for the
selected response; run probes through `ModelClient` before obtaining it:

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

This helper drains the stream and returns a `StreamSummary` with accumulated
text and complete tool calls. While it drains the designated
`PlannerModelClient` call, the runtime sends each validated text and thinking
fragment directly to the session stream so clients can display progress without
waiting for inference to finish.

`ConsumeStream` accepts the `*model.ValidatedStream` returned by every public
model client. Provider and transport adapters capture a `model.RequestContract`
before inference and pass their internal `model.Streamer` to `ValidateStream`;
planner code never wraps or revalidates streams.

Each runtime-managed model call creates an isolated response candidate before
planner code receives the response or stream chunk. Only the one call made
through `PlannerModelClient` may produce live UI events. The planner activity
allocates one response ID before planner or provider work starts. Every text and
thinking fragment from the designated call carries that ID and bypasses the run
log, hook bus, and memory. There is no response-started or response-discarded
lifecycle. Once a client accepts text, no later validation, planner decision,
or run event can remove or replace it. Assistant and thinking events still pass
through the configured `StreamProfile`.

Generated Plan and Resume activities use one Temporal attempt, and agent
registration rejects retry policies that could execute either activity more
than once. A retry after visible output could generate different text under the
same activity result identity. Tool execution retains its separate retry policy
because stable tool-call identity lets the tool owner replay a completed result
without repeating an external effect. An explicit model-output recovery or
workflow continuation is a new planner activity and therefore receives a new
response ID.

Text and thinking fragments are sent as soon as they arrive because delaying or
combining them would remove the real-time behavior. Tool argument fragments
remain inside the model validation boundary: partial JSON is neither executable
nor independently useful, so only the complete validated tool call reaches the
planner and workflow.

Every successful stream ends its typed chunks with clean EOF and
exposes exactly one canonical response through `ValidatedStream.Response()`. The
runtime captures and validates that response before returning EOF to planner
code, including when the provider is behind a model gateway. Incomplete
provider content blocks are contract errors. A validated stream is tied to the
model identity, structured-output contract, tool definitions, and generated
validators copied before provider work begins. Request mutation after that
point cannot change which output the stream accepts.

Tool definitions built from a generated `tools.ToolSpec` retain that tool's
generated payload decoder inside the process. Unary responses and final
streamed tool-call chunks must name a tool present in the request and satisfy
the checks described in
[Model-Visible Tool Arguments](#model-visible-tool-arguments) before planner
code receives them. Caller-authored tools built with
`model.AdvertisedToolInputFromSchema` compile their JSON Schema once and apply
it at the same point. Requests reject tool definitions that carry schema bytes
without an input validator.
When `PlanResult` contains tool calls, the runtime
matches their model-facing IDs, names, and payload bytes to exactly one
candidate and persists only that response's assistant transcript. Mixed,
copied, incomplete, or ambiguous provider outputs fail the planner activity.
Selecting a provider message as `FinalResponse` also requires the result to
preserve that response's complete tool-call set; a terminal result cannot
silently discard a provider-requested action.
Provider adapters set `OutputLimited` on the complete `model.Response` and its
terminal `StopChunk` when the provider reports that generated output reached a
token or context limit. The planner decides whether that exact complete final
response is acceptable; the runtime does not replace a planner-accepted answer.
An output-limited tool batch remains invalid because it cannot prove that the
provider completed the requested calls, so no tool from that batch executes.
Call order has no commit semantics: planners may probe with `ModelClient`, then
make exactly one selected call through `PlannerModelClient`. Terminal helpers
return the selected provider message without exposing transcript identity or
matching mechanics. Later session turns therefore replay the provider's signed
thinking while only the selected provider response enters durable history.
Live fragments are not history records themselves. If their response is
rejected or fails after text was delivered, the aggregate delivered text is
stored and restored after reload; malformed tool calls and incomplete provider
metadata are not stored with it. A caller cancellation or deadline can still
leave unfinished text only in the current client because canceled activity work
cannot commit new records. Usage events still include every invocation. After atomic
tool-batch admission, the workflow commits the complete selected response once
before any effects. The committed `assistant_turn` carries the activity response
ID and the exact ordered assistant messages stored in the run log. If live
fragments exist, their ordered text is an exact prefix of the text in those
messages, so a downstream client can append only the missing suffix without
replacing text. The workflow publishes each accepted ordered record batch through one
`runtime.store` activity; stable event keys make a retried prefix idempotent without
creating one Temporal activity per record. Keyed stream publications use the
same identity, so a retry completes a failed delivery without appending a
duplicate client event.

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
transcript without charging the tool-call budget.
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
generatedSpecs := toolsetpkg.Specs()
toolSchemas := make([]*genregistry.ToolSchema, len(generatedSpecs))
for i, spec := range generatedSpecs {
    description := spec.Description
    toolSchemas[i] = &genregistry.ToolSchema{
        Name:                   string(spec.Name),
        Description:            &description,
        Tags:                   spec.Tags,
        PayloadSchema:          spec.Payload.Schema,
        ExecutionPayloadSchema: spec.ExecutionPayloadSchema,
        ResultSchema:           spec.Result.Schema,
    }
}
handler := toolsetpkg.NewProvider(serviceImpl)
providerID := podName + "/" + toolsetID
admissionRevision := mustRequiredEnv("TOOL_REGISTRY_ADMISSION_REVISION")
go func() {
    err := toolprovider.Serve(ctx, pulseClient, toolsetID, handler,
        toolprovider.Registration{
            AdmissionRevision: admissionRevision,
            Register: func(ctx context.Context, toolset, providerID, incarnationID, admissionRevision string) (toolprovider.RegistrationLease, error) {
                schemaFingerprint, err := toolsetpkg.SchemaFingerprint(toolset)
                if err != nil {
                    return toolprovider.RegistrationLease{}, err
                }
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
                    SchemaFingerprint: schemaFingerprint,
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
- **Service provider loop**: executes tools using generated provider handlers and submits terminal results through generated registry client callbacks

Import `goa.design/goa-ai/runtime/toolregistry` as `registrywire` in provider
and registry-consumer composition roots.
`runtime/toolregistry.WireProtocolVersion` is the only accepted registry
message-envelope version. Every provider Register payload and every consumer
CallTool or RetryTool payload must carry it. Consumers do not Register;
CallTool performs initial admission, while RetryTool can republish only one
existing exact admission after overload.

Construct registry clients with Goa's generated gRPC transport and service
types:

```go
import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	genregistrygrpc "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func newRegistryClient(address string) (*genregistry.Client, func() error, error) {
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to registry: %w", err)
	}
	transport := genregistrygrpc.NewClient(conn, grpc.WaitForReady(true))
	return genregistry.NewClient(
		transport.Register(),
		transport.ReleaseProvider(),
		transport.DrainProvider(),
		transport.Unregister(),
		transport.Pong(),
		transport.ListToolsets(),
		transport.GetToolset(),
		transport.CheckAdmission(),
		transport.Search(),
		transport.CallTool(),
		transport.RetryTool(),
		transport.CompleteToolCall(),
		transport.PublishToolOutputDelta(),
		transport.ReportToolCallOverload(),
		transport.ClaimToolCall(),
	), conn.Close, nil
}
```

The former `runtime/registry.GRPCClientAdapter` accepted the generated protobuf
stub and owned protobuf-to-runtime conversion. It was removed. Provider,
invocation, and direct discovery callers use the complete generated service
client above. Applications that use `runtime/registry.Manager` pass that same
service client to `runtime/registry.NewClient`; the transport-neutral wrapper
projects generated discovery results into the manager's cached and federated
resource types. The application retains ownership of the gRPC connection in
both cases.

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

After every admitted exit, `Serve` stops renewal and atomically marks the
original admitted token/incarnation lease draining before it closes the shared
sink. Workers finish only calls whose registry claims committed before
draining began. Buffered, unclaimed request events remain pending for a
replacement provider; draining rejects new claims while preserving authority
to complete previously claimed calls. The Drain request carries the
configured shutdown duration plus `SettlementAuthorityMargin`, and Redis
extends the lease from the transition time through that authority window.
`Serve` retains the renewal result before cleanup. If renewal returns a
different token at the same time as process cancellation, cleanup drains that
token concurrently under the same deadline and reports the token change as a
terminal contract failure. The changed token is released only when its drain
and the remaining settlement both succeed; otherwise the returned error
preserves the failed cleanup and lease expiry removes it.
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
If the transport loses the first response, the generated client retries the
same idempotent request and claim-operation ID. That exact operation returns
`execute` again. A later Pulse redelivery creates a different operation ID and
returns `claimed`, even when the provider, lease, and request event are the
same.
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
requires a coordinated release because registry servers and consumers do not
negotiate envelope versions. No deployment component persists registration
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
while model payload, registry execution payload, result, and sidecar schema
bytes are hashed exactly. Generated raw schemas must therefore be canonical:
reformatting semantically equivalent schema JSON changes schema and admission
identity.

This version is an exact wire fence, not capability negotiation. There is no
optional fallback or dual decoder. The registry rejects a missing version
(protobuf zero) or a mismatched version at the generated transport boundary and
repeats the check at the first line of CallTool or RetryTool, before catalog
lookup, health checks, result-stream creation, call admission, or Pulse
publication. Register and renewal apply the same check before provider
admission. The runtime-owned version therefore fences both producers of
protocol bytes while the version-bound registration token fences provider
generations.
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
lease may settle already-claimed work it owns.
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

Every retained call hash has an explicit `admitted` or `rejected` decision.
Missing and unknown decisions are protocol errors; reads reject them without
mutating the record.

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

Catalog leases, token fencing, flat shared streams, and incompatible-admission
non-overlap are permanent runtime contracts. Catalog and queued records must
match the current wire protocol; unknown records are rejected.

### Registry-Routed Execution (Agent/Consumer Side)

On the consumer side (an agent calling registry-routed toolsets), the runtime needs a `ToolCallExecutor` that:

- calls the registry gateway to publish the tool request and get `(tool_use_id, registration_token, execution_deadline, result_stream_expires_at)`, then
- subscribes to the retained per-call result stream but waits only through the absolute execution deadline before decoding the result using the compiled tool specs/codecs.

Goa-AI provides a reusable executor implementation in `runtime/toolregistry/executor` that implements `runtime.ToolCallExecutor`:

```go
import (
    toolregexec "goa.design/goa-ai/runtime/toolregistry/executor"
)

exec, err := toolregexec.New(registryClient, pulseClient, "myservice.helpers", specs)
if err != nil {
    return err
}

// Use exec.Execute as the executor for registry-backed toolsets.
```

`executor.Client` receives `toolregistry.ToolCallMeta` by value. A handwritten
adapter to the generated registry client must copy every field, including
`Labels`, into both `CallToolPayload.Meta` and `RetryToolPayload.Meta`:

```go
func registryMeta(meta toolregistry.ToolCallMeta) *genregistry.ToolCallMeta {
    return &genregistry.ToolCallMeta{
        RunID:            meta.RunID,
        SessionID:        meta.SessionID,
        TurnID:           meta.TurnID,
        ToolCallID:       meta.ToolCallID,
        ParentToolCallID: meta.ParentToolCallID,
        Labels:           maps.Clone(meta.Labels),
    }
}
```

Use the same conversion for a retry. Labels are part of the immutable call
identity, so changing or omitting them on a retry is rejected.

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
        // Decode call.Payload and use it for execution. Return structured field
        // issues for correctable validation failures; the workflow supplies the
        // rejected model input and generated example.
    },
    Specs: []tools.ToolSpec{...},
}
rt.RegisterToolset(reg)
```

**Agent-as-tool** — Nested agent execution:

```go
reg := runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
    Definition: nestedagent.Definition(),
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
   optional, or non-`String`. Named Goa `String` types are valid. A field that
   uses `struct:field:type` to replace its generated Go type is rejected because
   injection supplies a string and the replacement type defines no general
   string conversion contract.
2. Codegen time: each name is classified against the fixed
   `runtime.ToolCallMeta` field set (`sessionId`/`session_id` -> `SessionID`,
   `runId`/`run_id` -> `RunID`, `turnId`/`turn_id` -> `TurnID`,
   `toolCallId`/`tool_call_id` -> `ToolCallID`,
   `parentToolCallId`/`parent_tool_call_id` -> `ParentToolCallID`). A match is
   **meta-backed**; anything else is **label-backed**, using the design name
   verbatim as the label key. `codegen/agent/prepare.go`'s `flattenAndHide`
   removes the field from the model-facing schema and required list. The public
   generated tool input keeps the required field, and injection fills it after
   the model-visible JSON is decoded. Each toolset's `inject.go` (beside its
   `codecs.go`/`specs.go`) gets
   one generated `Inject<Tool>(p *<Tool>Payload, meta runtime.ToolCallMeta,
   labels map[string]string) error` function per injecting tool: meta-backed
   fields read from `meta`; label-backed fields look the key up in `labels`.
   Both sources run the field's declared Goa validation before assignment and
   return a precise error naming the tool and field when a value is invalid.
   Missing labels name the required key. The toolset's
   `RequiredLabels` (sorted, deduplicated label keys) is generated onto its
   specs package and included in the agent's immutable `AgentDefinition`.
3. Runtime time: both execution topologies call the **same** generated
   `Inject<Tool>` function between decode and execute, so population never
   diverges by where a tool runs:
   - Local (in-process) execution: the generated service executor calls
     `Inject<Tool>` immediately after decoding the tool payload, before any
     `WithPayloadMapper` customization or method-payload conversion, using
     the run's `ToolCallMeta` and runtime-owned `ToolCall.Labels`.
   - Registry-served (bound) tools: the generated provider (`provider.go`)
     calls the same `Inject<Tool>` function with the call metadata and its run
     labels.
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
4. Run start: the generated `AgentDefinition` carries the complete, deduplicated
   required-label list to both callers and workers. `Start` and `StartOneShot`
   validate `WithLabels(...)` before scheduling planner or tool work and report
   every missing key in one error. Remote callers therefore enforce the same
   contract without registering the worker locally. Continuation preparation
   checks the restored labels against the same definition.

**`WithLabels` contract:**

```go
out, err := client.Run(ctx, sessionID, messages,
    runtime.WithRunID(runID),
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

Generated tool specifications keep two input schemas. `Payload.Schema` is the
closed input the model may write. `ExecutionPayloadSchema` is the closed payload
sent through the registry after the runtime restores continuation fields. It
still omits fields declared with `Inject`, because the provider adds those only
after receiving the call. Provider registration sends both schemas, the
registry includes both in admission identity, and `CallTool` validates the
restored payload only against the execution schema. Neither schema is derived
or repaired at runtime. For a dedicated continuation, the first tool's
execution schema rejects a cursor and the continuation tool's execution schema
requires the restored cursor.

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

That omission object changes only the bounded content sent to the model. The
runtime store, continuation checkpoint, child result, final result, and stream
event retain the complete successful JSON. Planner resume activities carry
exact run-record identifiers and load the stored JSON instead of copying large
results through workflow history.

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
3. Runtime starts the child workflow using the route in its generated
   `AgentDefinition`
   Temporal terminates the child if its parent workflow closes first.
4. Child agent executes its own plan/execute loop
5. Runtime returns a parent `ToolResult` derived from the child run output (final text and/or finalizer output, plus aggregated telemetry). **Artifacts are not propagated to the parent tool result**; they remain attached to the child tool events.
6. `ChildRunLinked` event links parent and child for streaming

### Configuration

```go
reg := runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
    Definition:   dataanalystagent.Definition(),
    SystemPrompt: "You are a data analysis expert.",
    AgentToolContent: runtime.AgentToolContent{
        Templates: compiledTemplates, // Per-tool user message templates (optional)
        Texts:     textMessages,      // Alternative to templates (optional)
    },
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

Before completing the workflow, the runtime stores the suspension, suspended
status, and matching record together under the completed run ID. The checkpoint
can contain private planner messages and tool state; do not send it to an
untrusted client.
Before claiming an answer, the owning service prepares the continuation. This
loads the exact saved suspension, checks the response and every nested child
checkpoint against the current generated agent definitions, and takes an
immutable copy of the complete workflow input. It does not write runtime state
or call the workflow engine. A rejected response therefore leaves the requested
run ID unused.

The owning service then atomically accepts one prepared answer, so concurrent
answers cannot start two workflows from the same state, and starts that exact
prepared value. It may safely retry the same prepared value after an uncertain
engine response because `StartPrepared` submits the same stored request each time:

```go
prepared, err := client.PrepareContinuation(
    ctx,
    "session-1",
    previous.RunID,
    "run-124",
    "turn-2",
    response,
    runtime.WorkflowOptions{},
)
if err != nil {
    return err
}
preparedBytes, err := prepared.MarshalBinary()
if err != nil {
    return err
}
// This application-owned method uses one database transaction to accept the
// answer and store the prepared workflow ID with preparedBytes.
err = workflowStarts.AcceptContinuation(ctx, previous.RunID, prepared.RunID(), preparedBytes)
if err != nil {
    return err
}
```

A later process can submit the accepted request without retaining any in-memory
state from preparation:

```go
preparedBytes, err := workflowStarts.Load(ctx, "run-124")
if err != nil {
    return err
}
stored, err := runtime.ParsePreparedRun(preparedBytes)
if err != nil {
    return err
}
handle, err := client.StartPrepared(ctx, stored)
```

Initial runs use the same durable command. Call `Prepare`, store the bytes from
`MarshalBinary`, restore them with `ParsePreparedRun` after a restart, and call
`StartPrepared`. `PreparedRun.RunID` returns the exact workflow ID to associate
with the stored command. `Start` prepares and submits in memory when the
application does not need a durable write between validation and engine
submission.

`Prepare` and `PrepareOneShot` are client-only operations. They copy and
validate a complete request without writing runtime storage, sealing worker
registration, or calling the workflow engine. `PrepareContinuation` first reads
the saved suspension but still performs no write and starts no workflow.
Preparation does not serialize the optional stored form. `MarshalBinary`
creates that form; `StartPrepared` independently submits the engine request.
`Start`, `StartOneShot`, and `Continue` never call `MarshalBinary`.

Prepared bytes can contain the complete transcript, tool results, and private
continuation checkpoint. Store them only in trusted, access-controlled
application storage. Workflow launch settings are stored only in the engine
request; `api.RunInput` contains only workflow input. One workflow start may use
at most `engine.MaxPayloadBytes` (1,048,576) bytes. This inclusive count covers
the workflow ID, workflow and queue names, memo and search-attribute names, the
encoded workflow input, every memo and search value's data and metadata, and
the reserved digest memo added by the engine. Exactly 1,048,576 bytes is
accepted and one more is rejected. The stored JSON record has its own
8,388,608-byte ceiling for its complete representation, including the agent ID,
explicit queue override, JSON escaping, base64 expansion, and JSON field syntax.
`MarshalBinary` checks the complete record when it creates the bytes, and
`ParsePreparedRun` checks it before decoding. This second limit protects the
stored format; it does not increase the workflow request limit. Store large
domain values separately and pass durable references instead.

`MarshalBinary` reports a storage-encoding failure as
`ErrPreparedRunRejected` without changing the in-memory prepared request. It
remains startable, but an application that requires durable admission must not
start it until the bytes are stored. `ErrPreparedRunRejected` from
`ParsePreparedRun` means the stored bytes are malformed or are not the exact
format produced by `MarshalBinary`; they cannot be retried. `StartPrepared`
returns the same error when the stored
request no longer satisfies the current generated agent definition; that
command cannot start with this generated release. It also returns the error
when valid bytes are passed to the wrong generated agent client. The bytes
remain valid and must be submitted through the matching client.
`ErrWorkflowStartFailed`
means the engine did not confirm the start. Goa-AI does not retry automatically;
the application explicitly calls `StartPrepared` again with the same value, or
parses and submits the same stored bytes after a restart. If the cause is
`engine.ErrWorkflowStartConflict`, another request owns the workflow ID and
retrying cannot succeed.

Callers that do not need a separate application write can use `Continue`, which
prepares, starts, and waits for completion:

```go
next, err := client.Continue(
    ctx,
    "session-1",
    previous.RunID,
    "run-124",
    "turn-2",
    response,
    runtime.WorkflowOptions{},
)
```

`Runtime.LoadRunSuspension` exposes a checkpoint only after run metadata records
the predecessor as suspended. Running and paused runs return
`runtime.ErrRunSuspensionNotReady`; completed, failed, and canceled runs return
`session.ErrRunSuspensionNotFound`. A suspended run returns
`runtime.ErrRunSuspensionCorrupt` only when its checkpoint is missing, malformed,
inconsistent with its stored ID, invalid, or names another predecessor. Errors
while reading the runtime store remain unchanged, so callers do not mistake an
unavailable dependency for permanent checkpoint corruption.

One continuation consumes only the first item in `Suspension.Pending`. If more
input remains, the new workflow ends with another suspension. The checkpoint
restores the original messages, policy, labels, nested-tool identity, remaining
active-time budget, and exact call/result provenance; callers cannot override
those values. The runtime loads the suspension by predecessor run ID and checks
the checkpoint version, public pending requests, saved planner result, required
labels, every saved payload and result, and every nested child suspension before
starting the workflow. Caller and worker use the same generated
`AgentDefinition`, including the definitions of every reachable child agent.
Removing a tool or changing its generated codec deliberately makes a suspension
that depends on the old contract incompatible. The workflow checks the saved
input again before using it. If
the response closes a tool call created by the previous
workflow, the `tool_end` event belongs to the new result run and its required
`call_run_id` identifies the run that emitted the matching `tool_start`.

### Coordinated Generated-System Releases

Generated agents, generated completion packages, runtime workers, and their
callers use one exact contract and deploy as one release unit.

#### Preview upgrade guide

This preview intentionally changes the generated MCP API and the runtime
storage API. Upgrade all generated packages, runtime workers, and callers
together. Mixed old and new processes are not supported.

For MCP services:

- Add a service-level `JSONRPC` block with one `POST` route. `MCP(...)` no
  longer chooses an HTTP path implicitly.
- Replace `WatchableResource` with `Resource` when the method is a fixed unary
  read. Generated resource subscriptions are no longer supported.
- Replace `DynamicPrompt` with `StaticPrompt` only when the prompt is fixed in
  the design. Goa-AI no longer generates dynamic prompt providers.
- Remove uses of the generated `Notification`, `Subscription`, and
  `SubscriptionMonitor` APIs. This preview has no generated replacement for
  server notifications or subscriptions.
- Remove `AllowedResourceURIs`, `DeniedResourceURIs`,
  `StructuredStreamJSON`, and `ProtocolVersionOverride` from generated
  `MCPAdapterOptions`. Enforce resource authorization in the Goa service, return
  the declared result shape, and set the protocol version in the design.
- Replace `SSECaller` and `NewSSECaller` with `HTTPCaller` and
  `NewHTTPCaller`. The HTTP caller accepts a normal JSON response or an HTTP
  event-stream response for a unary call.
- Replace direct uses of `runtime/mcp.Notification`, `Broadcaster`,
  `Subscription`, `NewChannelBroadcaster`, `EncodeJSONToString`, or
  `CoerceQuery` with application-owned code if it is still needed. These
  helpers supported the removed generated notification and subscription paths
  and have no Goa-AI replacement.
- Regenerate the whole service. Do not preserve or copy old generated MCP
  packages; their constructors, adapters, and transport files are generated
  from the new contract.

For tool registry wire protocol 10:

- Regenerate registry clients and providers together. Registration must send
  both generated input schemas: `Payload.Schema` describes arguments accepted
  from the model, while `ExecutionPayloadSchema` describes the payload sent to
  the provider after continuation handling. A handwritten
  `executor.Client` adapter must copy `ToolCallMeta.Labels` into both the first
  call and every retry, as shown in
  [Registry-Routed Execution](#registry-routed-execution-agentconsumer-side).
- Before replacing protocol 8 or 9, prevent consumers from starting new tool
  calls and let every admitted call finish. Prove that retained call records
  are terminal, the global and per-provider settlement indexes are empty, and
  each provider consumer group has no pending messages or lag. Then stop every
  old registry, provider, and consumer process. Mixed versions reject each
  other instead of translating the required registration schema.
- Save the catalog hash before changing it. For a registry named `<name>`, the
  hash is `map:<name>:toolsets:content`. Delete only hash fields whose names
  start with `registry:toolset:`. Keep Pulse's `=rev` and `=kind` fields. The
  following atomic Redis command returns every field it removes:

  ```text
  EVAL "local fields=redis.call('HKEYS',KEYS[1]); local removed={}; for _,field in ipairs(fields) do if string.sub(field,1,17)=='registry:toolset:' then redis.call('HDEL',KEYS[1],field); table.insert(removed,field); end; end; return removed" 1 map:<name>:toolsets:content
  ```

  Verify afterward that
  `HSCAN map:<name>:toolsets:content 0 MATCH registry:toolset:* COUNT 1000`
  returns no matching fields and that `HMGET map:<name>:toolsets:content =rev
  =kind` still returns the reserved map state.
- Do not delete retained call records, settlement records, or Pulse streams.
  Once drained, they do not block protocol-10 startup and retain the evidence
  needed to prevent an old call from executing twice. Their configured expiry
  removes call-specific state.
- After the old processes have stopped and the catalog fields have been removed,
  start the protocol-10 registry, providers, and consumers as one release. Then
  verify every expected toolset registered with protocol 10 before accepting new
  work. The catalog contains no product data; providers recreate its schemas,
  leases, health, and tokens. The operation discards protocol-8 and protocol-9
  retirement history, so rollback to either older protocol is unsupported after
  protocol-10 traffic starts.

For runtime storage and workflow adapters:

- Regenerate every agent package. Generated callers and workers now share one
  immutable `AgentDefinition` containing the route, tool contracts, required
  labels, completion policy, and every reachable child definition.
- Replace handwritten `AgentRegistration` route, tool-specification, and
  required-label fields with `Definition`. Keep only worker implementations and
  activity settings in the registration. Remove `WorkerConfig`, `WorkerOption`,
  `WithWorker`, and `WithQueue`; the generated definition owns the workflow name
  and default task queue. `WithTaskQueue` remains available on an individual
  start because that queue is part of the caller's exact start request.
- Pass the generated child definition to handwritten `AgentToolConfig` values
  and generated exported-agent registration helpers. Remove separate child
  agent IDs, routes, queues, activity names, specifications, and required labels
  from those registrations.
- Replace a service-side load followed by a direct continuation start with
  `PrepareContinuation` and `StartPrepared`. Preparation validates and
  copies the complete input without calling the engine. Start submits only that
  prepared value. Use `Continue` only when no application write must occur
  between validation and start.
- Use `PreparedRun` for both initial and continuation starts.
  `PreparedRun.RunID` returns the workflow ID assigned during preparation,
  `PreparedRun.MarshalBinary` creates the optional durable form only when
  called, and `ParsePreparedRun` restores it after a process restart.
- `Prepare(sessionID, messages, opts...)` and
  `PrepareOneShot(messages, opts...)` are new methods. Handwritten
  `AgentClient` implementations must add both methods. Preparation performs no
  I/O, so neither method accepts a context. `Start`, `Run`, `StartOneShot`, and
  `OneShotRun` now use the same prepared-request path internally.
- Replace custom `RunOption` functions with the option constructors in
  `runtime`, such as `WithRunID`, `WithMemo`, and `WithTaskQueue`. `RunOption`
  is now a closed interface so external code cannot depend on private request
  construction. Remove nil placeholders from option lists; a nil `RunOption`
  now panics instead of being ignored.
- Remove `WithWorkflowOptions` from initial and one-shot starts. Use
  `WithTaskQueue`, `WithMemo`, and `WithSearchAttributes` so each option has one
  clear effect. Continuation methods now take `runtime.WorkflowOptions` by
  value; pass `runtime.WorkflowOptions{}` when no launch setting is needed.
  `api.WorkflowOptions` is removed because launch settings are caller-side
  values, not workflow input. `api.RunInput` no longer contains a
  `WorkflowOptions` field.
- Stop reading `RunInput.WorkflowOptions` from workflow handlers or custom
  workflow engines. The field no longer exists. Read memo, search attributes,
  and task queue from the corresponding `engine.WorkflowStartRequest` fields
  instead. Custom engines now receive memo entries as `engine.EncodedValue`;
  persist each entry's `Metadata` and `Data` bytes without decoding and encoding
  it again. Search attributes arrive in their final engine-wide types: `string`,
  `bool`, `int64`, `float64`, `time.Time`, or `[]string`. Integers and
  `float32` values are converted before the engine receives them. Any other
  type is rejected before submission.
- Populate `ID`, `Workflow`, `TaskQueue`, and `Input` on every
  `engine.WorkflowStartRequest`. All four fields are required. Official and
  custom engines reject a missing value instead of supplying a Temporal or
  local default. `ID` must equal `Input.RunID`.
- Populate `ID`, `Workflow`, `TaskQueue`, and `Input` on every
  `engine.ChildWorkflowRequest` too. `TaskQueue` is newly required for child
  starts. A child must name its own queue instead of inheriting the parent or an
  engine default, and its `ID` must equal `Input.RunID`. A child-start method
  now returns only after the engine accepts or rejects the start.
- Treat a zero `engine.RetryPolicy` as no retry override. `MaxAttempts` includes
  the first attempt. A policy that sets `InitialInterval` or
  `BackoffCoefficient` must also set a positive `MaxAttempts` or
  `UnlimitedAttempts`.
- Custom engines call `contract.NormalizeRootRequest` and bind its digest to the
  workflow ID for exact-retry checks. They call
  `contract.NormalizeChildRequest` for child starts. They use
  `contract.CopyRunInput` for every initial or retry handler attempt, retain one
  private `contract.CopyRunOutput` result, and copy that result again for every
  wait, query, or other caller-facing read. These functions enforce the same
  strict representation, 1 MiB limit, nil behavior, and independent ownership
  as the shipped engines.
- Every workflow input written by the old runtime includes the removed
  `WorkflowOptions` field, even when its value is nil. The new strict decoder
  rejects that old input shape, and the new exact-start identity differs from
  every old request. Finish or cancel every old workflow before upgrading.
  Resolve or abandon every old start whose result is uncertain, and never retry
  an old request or reuse its workflow ID with this runtime. A changed request
  returns `engine.ErrWorkflowStartConflict`.
- Replace separate `session.Store` and `runlog.Store` implementations with one
  `storage.Store`. Each method stores one complete lifecycle change and its
  ordered run record together.
- When `session.RunStart.PredecessorRunID` is non-empty, implement root and
  child starts so they require that run to exist, be suspended, and have the
  same session, agent, and parent. Check those facts in the same transaction as
  the successor start. Reject a mismatch before writing the successor or its
  parent link. Do not copy the predecessor ID into `RunMeta`; `RunStarted` is
  the immutable relationship record.
- Pass that store as the first argument to `runtime.New`. Remove
  `WithSessionStore` and `WithRunEventStore`; they no longer exist:

  ```go
  rt := runtime.New(store, runtime.WithEngine(engine))
  ```

- Replace `runtime/agent/session/inmem` and `runtime/agent/runlog/inmem` with
  `runtime/agent/storage/inmem` for local work and tests. The former
  `features/session/mongo` and `features/runlog/mongo` packages are removed;
  production applications now implement their own durable `storage.Store`.
- Convert existing physical records or collections before the new runtime
  starts if the new store cannot read their current layout. Completed run
  outcomes keep the same meaning; the conversion changes storage layout, not
  history.
- Convert every stored `RunStarted` payload offline before starting the new
  runtime. The old payload stored `RunContext` and `Input`. The new payload
  stores only `parent_run_id`, optional `predecessor_run_id`, and `labels`; copy
  the parent and labels from the old `RunContext` and discard the duplicated input.
  For every historical continuation, copy the exact run ID whose checkpoint it
  restored from the application's stored continuation identity. Initial runs
  and ordinary child runs leave `PredecessorRunID` empty. Do not infer a
  predecessor from timestamps, labels, or record order. Keep the run ID, agent
  ID, session ID, turn ID, event key, and timestamp in the surrounding run
  record. The new decoder rejects the old fields and every unknown field
  instead of silently ignoring them.
- Synthesize a `RunStarted` record before the canceled `RunCompleted` record for
  every historical run stopped because its Session had already ended. Every
  admitted run now has exactly one start record. The two records use different
  stable event keys and the same start timestamp, owner, and labels.
- Change `Runtime.ResolvePromptRefs(ctx, runID)` calls to
  `Runtime.ResolvePromptRefs(ctx, sessionID, runID)`. Pass the Session ID that
  the caller has already authorized. A run from another Session is reported as
  missing.
- Replace `WithRecordActivityTimeout` with `WithStorageActivityTimeout`. The
  new option covers the single storage activity that writes both lifecycle
  state and ordered run records.
- Remove `ToolCallArgsDeltaEvent` handling from hook and stream subscribers.
  Partial tool arguments remain private until the provider finishes the call
  and the advertised schema plus attached decoder accept it. Consumers observe
  the complete `ToolCallScheduled` event afterward.
- Remove `ResultOmitted` and `ResultOmittedReason` from API, planner, hook, and
  stream values. Successful typed tool results are stored in full, and planner
  activities load those exact bytes from run records by ID. There is no
  omitted-success state to handle.
- Remove the extra `context.Context` argument from custom workflow activity
  methods and `Await`. The `WorkflowContext` receiver now owns cancellation.
- Stop setting `policy.CapsState.ExpiresAt`. The workflow owns its budget and
  hard deadlines directly.
- Treat saved suspensions from versions before
  `goa-ai.run-suspension.v7` as incompatible. They cannot be resumed by this
  runtime.

Install the Goa revision required by this module before regenerating:

```bash
go install goa.design/goa/v3/cmd/goa@v3.31.0-preview.3
```

For a release that changes generated or persisted runtime shapes:

1. Regenerate every consumer from the same Goa-AI revision.
2. Finish or cancel every workflow started by the old runtime. Do not replay an
   old active workflow history with the new worker.
3. Resolve or abandon every start whose old caller did not receive a definite
   engine result. Do not retry old uncertain requests after the upgrade.
4. Deploy the runtime workers for every workflow and activity queue, generated
   packages, and callers as one release.
5. Verify every deployed component reports the same revision before accepting
   new work.

Completed run history keeps the same meaning. Saved suspensions must use the
current `goa-ai.run-suspension.v7` contract. A host may still need to convert
its physical records or collections so the new store can read them. That
conversion must preserve every recorded outcome and event.

A coordinated upgrade must replace every former session and run-record writer
with the current `storage.Store` contract in one release. The host owns its
schema, data conversion, verification, backup, and deployment plan. See
[Runtime Store](#runtime-store-storagestore) for the steady-state contract.
A release that introduces workflow recipe digests must also stop new
admissions and prove that no unresolved pre-upgrade start obligation or active
workflow still requires attachment by exact ID. Deploy every workflow starter
together before admission resumes. A queryable execution without the reserved
recipe memo is a conflict; the runtime never infers its original start request.

`goa-ai.run-suspension.v7` is the only accepted suspension schema. Every
model-authored await item preserves
its runtime `ToolCallID` separately from the provider `ModelToolCallID`: runtime
records and continuation responses use the former, while provider transcript
reconstruction uses the latter.

Ending a session stops future work but retains its run metadata for inspection.
When the owning application permanently deletes the session's application data, it
must wait for in-flight workflow and stream work to settle and then purge the
session through its own administrative API. Purging permanently reserves the
session ID before removing the session, every owned run, all private
checkpoints, and all ordered records. The integrated in-memory store exposes
these host operations for local development and tests.

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
			ID:              "clarify-device",
			ToolName:        tools.Ident("assistant.ask_clarification"),
			ModelToolCallID: call.ID,
			Payload:         call.Payload,
			Question:        "Which device should I configure?",
		}),
	),
}
```

The runtime emits the same `AwaitClarification` event for the UI. The next
workflow decodes the answer with the registered generated result codec and
restores a provider-valid `tool_use` / `tool_result` pair. Do not replace this
correlation with a reminder or a copied user message. The planner must supply
the provider's `ModelToolCallID` and leave `ToolCallID` empty. Before returning
the suspension, the workflow assigns a deterministic runtime `ToolCallID`.
Provider transcript parts retain `ModelToolCallID`; suspension responses and
runtime events use `ToolCallID`.

### External Tools

Planners can request tools that execute out-of-band:

```go
return &planner.PlanResult{
    Await: planner.NewAwait(
        planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
            ID: "external-1",
            Items: []planner.AwaitToolItem{{
                Name:            tools.Ident("external.fetch"),
                ModelToolCallID: call.ID,
                Payload:         json.RawMessage(`{"url":"..."}`),
            }},
        }),
    ),
}
```

Each item must carry its unique provider `ModelToolCallID` and leave
`ToolCallID` empty. The workflow adds a stable, distinct runtime `ToolCallID`
to each item before publishing and storing the suspension. Callers start the
next workflow with the exact result set, copying that runtime ID from the
suspension:

```go
response := &api.PendingInputResponse{
    ToolResults: &api.ToolResultsSet{
        ID: "external-1",
        Results: []*api.ProvidedToolResult{
            {
                ToolCallID: pending.Await.ExternalTools.Items[0].ToolCallID,
                Name:       tools.Ident("external.fetch"),
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
    "tool_name": "inventory.update_stock",
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

**Determinism note:** When using a durable workflow engine such as Temporal,
workflow code must be deterministic and must not trigger external I/O. The
runtime therefore routes every durable state change through the single
`runtime.store` activity. That activity stores run metadata, checkpoints, and
ordered records outside the workflow thread, then sends newly inserted hook
records to subscribers. Activities and other non-workflow code can perform I/O
directly.

**Event types:**

| Event | When |
|-------|------|
| `RunStarted` | Run begins; its stored payload contains `ParentRunID`, optional `PredecessorRunID`, and `Labels` |
| `RunCompleted` | Run finishes (success, failed, canceled); carries the run's start labels |
| `RunSuspended` | Workflow ended with a versioned checkpoint and ordered pending input |
| `RunPhaseChanged` | Phase transitions (planning, executing_tools, etc.) |
| `PromptRendered` | A successful recorded prompt render is accepted into a run |
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
`ModelOutputRejectedEvent.ReasonSHA256` and `ReasonSize` identify the private
validation-cause text without copying that text into Temporal or the run log.
Public error text remains fixed and generic; the fingerprint preserves durable
distinction between different causes without storing either cause.
`OutputValidationKind` is present only when an exact
`model.OutputValidationError` caused the rejection. Its eight closed values are
`response_shape`, `output_bounds`, `tool_identity`, `tool_arguments`,
`tool_choice`, `structured_output`, `stream_protocol`, and `usage`.
Planner-authored policy rejections and older records leave it empty.
`ModelResponsePresent` distinguishes a complete response from a
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
    MaxToolCalls           int
    RemainingToolCalls     int
    MaxRecoveryTurns       int
    RemainingRecoveryTurns int
}
```

The runtime passes each policy engine a positive `MaxRecoveryTurns`, using the
agent setting or the framework default. A policy decision may leave both
recovery fields at zero to keep the current state unchanged. Otherwise,
`MaxRecoveryTurns` must be positive and `RemainingRecoveryTurns` must be
between zero and that maximum; the runtime rejects an invalid decision.

### Per-Run Policy Overrides

Callers can override policy for specific runs:

```go
client.Run(ctx, "session-1", msgs,
    runtime.WithRunID("run-1"),
    runtime.WithRunMaxToolCalls(5),
    runtime.WithRunMaxRecoveryTurns(2),
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
    MaxToolCalls:     10,
    MaxRecoveryTurns: 2,
    TimeBudget:       5 * time.Minute,
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

### Runtime Store (`storage.Store`)

The runtime requires one store for run metadata, continuation checkpoints, and
ordered run records. The host's concrete repository also owns session creation,
ending, listing, and permanent deletion, but those administrative operations are
not part of the worker-facing `storage.Store` interface. A host may put that
repository behind a Session service and give agent workers a `storage.Store`
adapter built on the service's generated typed client. Agent workers must not
open or share the Session service's database.

Lifecycle commands store the state change and matching records together:

- `StartRootRun` always stores `RunStarted` after the engine accepts the
  workflow. If the Session has already ended, it then stores `RunCompleted`
  with canceled status. A continued run puts the run whose planner state it
  restored in the `RunStarted` record.
- `StartChildRun` always stores `ChildRunLinked` followed by `RunStarted`. If
  the Session has already ended, it then stores `RunCompleted` with canceled
  status.
- `StartOneShotRun` gives sessionless work the same durable run metadata and
  stores `RunStarted`.
- `StartOneShotChildRun` atomically stores `ChildRunLinked` on a running
  sessionless parent and `RunStarted` on its sessionless child.
- `RecordRunCancellation`, `RecordRunSuspension`, and `RecordRunTerminal` each
  store the run change and its matching record atomically.
- `AppendRunRecord` stores ordinary records and returns the session state
  observed in the same write. Streaming therefore never needs a second session
  lookup to decide whether the record may be published.

Exact start retries return the original immutable `StartOutcome` and ordered
record identifiers. Changed run, agent, session, or parent identity returns
`session.ErrRunConflict`. A record whose agent or session differs from its
stored run returns `storage.ErrRunRecordOwnerMismatch`.

Every mutation method returns `storage.ContractError` when repeating the same
command cannot succeed, including stored-state conflicts and invalid record
ownership. Custom stores must preserve this classification with
`storage.NewContractError`. Temporary database and network failures remain
ordinary errors so the storage activity can retry them.

Workflow code reaches the store through one `runtime.store` activity. Its
`StorageActivityCommand` sets exactly one of `Append`, `RootStart`,
`ChildStart`, `OneShotStart`, `OneShotChildStart`, `Cancellation`, `Suspension`,
or `Terminal`.
`StorageActivityResult` sets exactly the same field and no other field. The
runtime rejects empty commands, multiple commands, empty results, multiple
results, and a result field that does not match the command. This explicit
shape prevents the activity from guessing an operation by inspecting event
types.

Root and child starts serialize with session ending inside the host store. An
active session produces running metadata. An ended session produces terminal
canceled metadata with reason `session_ended`, so planner and tool work cannot
begin. A sessionful caller must supply a
stable run ID. The engine binds that ID to the exact start request while the
backend can still query the execution. During that period, an exact retry
returns the original open or closed execution and a changed request returns
`WorkflowStartConflictError`. Temporal forces duplicate starts to return an
error and compares an engine-owned digest from workflow memo. The shared,
versioned digest frames the caller-submitted workflow name, task queue, input boundary
payload, run timeout, retry policy, and every sorted memo and search-attribute
entry with its payload metadata and bytes. The in-memory engine stores only the
fixed-size digest and executes a converter-produced input snapshot on its own
cancelable context. Every root and child request requires its engine workflow ID
to equal `RunInput.RunID`. A zero `engine.RetryPolicy` supplies no override.
When a request supplies a policy, `MaxAttempts` includes the first execution;
retry timing requires a positive `MaxAttempts` or `UnlimitedAttempts`. After
backend history expires, the owning application uses
durable command identity to prevent reopening settled work; the engine does not
add a durable identity registry.

Hook persistence has one owner. Start and lifecycle commands store their
selected records. Other durable hook events use `AppendRunRecord`. The hook bus
sees only newly inserted records. An active session's live stream also receives
exact activity retries after a failed delivery; stable event keys let the sink
return its original publication instead of creating a duplicate. The bus has no
second lifecycle writer.

Prompt references and child relationships are derived from canonical ordered
records. `RunMeta` does not duplicate those values. `ChildRunLinked` contains
the tool and call that created the child plus the child run and agent. The
record envelope names the parent run, parent agent, and Session.

`Runtime.ResolvePromptRefs(ctx, sessionID, runID)` requires the Session ID that
the caller has already authorized. The earlier form without `sessionID` has
been removed; callers must pass the expected owner when they upgrade. The
empty Session ID selects a sessionless one-shot run that the caller has already
authorized by its run ID. The method reports any other owner as missing, then
reads the requested run's records breadth-first across every predecessor named
by its one `RunStarted` record and every `ChildRunLinked` child reachable from
those runs.
The predecessor is the suspended run whose saved planner messages the
continuation restored. It must keep the same Session, agent, and parent run as
its successor. Each child must keep the same Session and match the child agent
and parent run named by its link record. A missing or mismatched related run, a
relationship cycle, or more than one start record is a stored-data error; it is
never skipped. A run stopped because its Session had already ended has
`RunStarted` followed by a canceled `RunCompleted` record. The resolver
validates both records against the stored run, including their owner, labels,
reason, and start time. That run does no planner or tool work, so it contributes
no prompt references or child relationships. The method collects each
`PromptRendered` reference once and visits each run once. These records show
which prompt versions and scopes contributed to the run. Exact rendered prompt
text remains in immutable workflow input or workflow history and is not
reconstructed from the references.

Child workflow IDs are single-use. Every second explicit issue is rejected for
open and closed children, including an otherwise identical request; Temporal
deterministic replay is not a second issue.

Cancellation provenance is written before engine cancellation. The first reason
is immutable for every run type. Cancellation metadata plus
`engine.ErrWorkflowNotFound` is an invariant error for an active run. If both
metadata and the workflow are absent, cancellation is idempotently complete. If
the run became terminal just before cancellation was recorded, cancellation is
also idempotently complete.

The ordered records are the canonical history used for introspection,
audit/debug UIs, continuation hydration, and compact `run.Snapshot` values.
Records are append-only until the host permanently purges their ended session.

Pages are always returned oldest-first. `Event.ID` and page cursors are
store-owned opaque strings: callers may retain and return them to the same
listing method, but must not parse them or derive time from them. A store must
keep that order stable independently of event timestamps.

The runtime exposes:

- `Runtime.ListRunEvents(ctx, runID, cursor, limit)` for cursor-paginated listing
- `Runtime.GetRunSnapshot(ctx, runID)` for a compact snapshot derived from replaying the run log
- `Runtime.EnsureChildRunLink(ctx, runID)` to validate and redeliver one
  session-backed child's exact stored parent link without delivering its final result
- `Runtime.EnsureRunCompletion(ctx, runID)` to store a completion missing from
  an active run or redeliver the exact result of a run that is already closed

The first two operations are read-only. They never infer or store a missing
terminal result. Normal workflow completion retries suspension and terminal
writes until the store accepts them. `EnsureRunCompletion` is a separate
operator command. For an active run, it verifies that engine history is closed,
retrieves the final workflow output and workflow error, and stores the missing
suspension or terminal record through a repair-only store method. For a closed
run, it validates and redelivers the exact stored result. If the workflow stores
a different final record while the command is running, the command reloads and
redelivers that stored winner instead of publishing its reconstructed result.
For a child run, it first validates the stored child start and redelivers the
exact stored parent link. This guarantees that stream consumers receive child
attribution before the terminal child event. A failure to retrieve the engine
result is returned to the operator and is never stored as the workflow's final
error.
`EnsureChildRunLink` lets a host restore nested links in parent-first order
before it redelivers final results in stored order. New child starts require a
running parent; exact retries of already stored child starts remain valid after
the parent stops.
Both ensure commands require `Runtime.WithStream` while the Session is active.
When the store response reports that the ensure command inserted the missing
completion, the runtime notifies the local hook bus once before stream delivery.
Retrying the command redelivers only the exact stored stream events. An ended
Session retains its durable result and suppresses stream delivery. The Session
status returned with the stored record decides whether delivery is required. An
event accepted while active remains due even if the Session ends during a retry.
The engine supplies one stable workflow completion time. A recovered record
uses that time, so a retry submits the exact same timestamp.
Every accepted lifecycle timestamp uses millisecond precision because runtime
records carry time as integer milliseconds. Stores reject finer timestamps
instead of changing them, so an exact retry always carries the same value.

Pass the required store as the first argument to `runtime.New`. Goa-AI provides
`runtime/agent/storage/inmem` for local development and tests. Production hosts
implement the same contract in their own durable repository.

The host's concrete store owns session administration. Ending prevents future
root and child work while retaining history. Purge rejects an active session or
one with active runs, permanently reserves the session ID, and then removes its
runs, checkpoints, and records. A production implementation may delete data in
bounded restartable batches after the durable reservation. Other sessions and
sessionless runs remain readable.

The run log is also the canonical hydration source for planner resumes:
`ToolCallScheduledEvent` stores the authoritative tool payload, and
`ToolResultReceivedEvent` stores the authoritative result JSON plus
planner-visible outcome metadata and server-only sidecars once. Planner
activity inputs now carry tool-call references only and reload canonical state
on demand instead of accumulating duplicated summaries in workflow history.
Hydration accepts the pair only when call ID, tool name, parent call, session,
agent, and scheduling run identity match exactly.

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
- Bedrock uses Runtime `CountTokens` when the resolved model supports it.
  Claude Opus 4.7, Opus 4.8, Opus 5, Sonnet 5, and Mythos 5 require
  `bedrock.Options.MantleTokenCounter` instead. `bedrock.New` and
  `bedrock.NewProvider` reject a configuration that assigns one of these
  models to the default, high-reasoning, or small class without that counter.
  Before delegating, the Bedrock adapter resolves the foundation model ID and
  preserves the effective tools. Mantle counts the same private non-strict
  tool that Converse receives for these models. Native `OutputConfig` remains
  unsupported in Mantle. Claude 4.6 structured output uses the same strict
  tool in Runtime counting and Converse instead. Provider validation errors
  remain errors; the adapter never parses an error message into a fabricated
  count.
- `bedrock.NewAnthropic` also resolves structured output before counting.
  Sonnet 5 and Opus 5 send one private non-strict tool to both `InvokeModel`
  and Mantle, then expose the tool payload as a canonical completion. Models
  that keep native `output_config.format` require a counter endpoint that
  accepts that same native field.

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
    ID:              "pending_items",
    Text:            "Review pending items before proceeding.",
    Priority:        reminder.TierGuidance,
    Attachment:      reminder.Attachment{Kind: reminder.AttachmentUserTurn},
    MaxPerRun:       3,
    MinTurnsBetween: 2,
})

// Remove when no longer relevant
input.Agent.RemoveReminder("pending_items")
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
content, err := input.Agent.RenderPrompt(ctx, "assistant.system", map[string]any{
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

rl := mdlmw.NewOutputReservationAdaptiveRateLimiter(
    ctx,
    throughputMap,     // *rmap.Map for cluster-wide state (nil for local)
    "bedrock:sonnet",  // Model family key
    80_000,            // Initial quota-token capacity per minute
    1_000_000,         // Maximum quota-token capacity per minute
)

limitedClient, err := rl.Middleware()(modelClient)
if err != nil {
    return err
}
if err := rt.RegisterModel("bedrock", limitedClient); err != nil {
    return err
}
```

`NewAdaptiveRateLimiter` charges only the provider's exact input-token count.
`NewOutputReservationAdaptiveRateLimiter` adds `Request.MaxTokens` and rejects
requests that omit a positive `MaxTokens`. Use the reservation constructor when
the provider deducts requested output capacity from the same per-minute quota
before generation. Its versioned cluster key keeps the two accounting modes
separate during rolling upgrades. For streams, the limiter increases capacity
only after clean end-of-stream and reduces capacity when a terminal stream error
is rate limited.

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
    RegisterStorageActivity(ctx, name, opts, fn) error
    RegisterPlannerActivity(ctx, name, opts, fn) error
    RegisterExecuteToolActivity(ctx, name, opts, fn) error
    RegisterAgentChildActivity(ctx, name, opts, fn) error
    StartWorkflow(ctx, req WorkflowStartRequest) (WorkflowHandle, error)
    QueryRunCompletion(ctx, runID string) (RunCompletion, error)
}

```

### WorkflowContext

Workflow handlers receive a context for deterministic operations:

```go
type WorkflowContext interface {
    Context() context.Context
    SetQueryHandler(name string, handler any) error
    SetCancellationHandler(handler engine.CancellationHandler) error
    WorkflowID() string
    RunID() string
    Now() time.Time  // Deterministic time
    NextSequence() uint64
    ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error)
    ExecutePlannerActivity(call engine.PlannerActivityCall) (*api.PlanActivityOutput, error)
    ExecuteToolActivity(call engine.ToolActivityCall) (*api.ToolOutput, error)
    ExecuteToolActivityAsync(call engine.ToolActivityCall) (Future[*api.ToolOutput], error)
    ExecuteAgentChildActivity(call engine.AgentChildActivityCall) (*api.AgentChildActivityOutput, error)
    NewTimer(ctx context.Context, d time.Duration) (Future[time.Time], error)
    Await(condition func() bool) error
    StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error)
    Detached() WorkflowContext
    WithCancel() (WorkflowContext, func())
}

```

Custom engine adapters must implement `RegisterStorageActivity` and
`ExecuteStorageActivity`. The runtime registers one typed `runtime.store`
activity. Each `StorageActivityCommand` has exactly one `Append`, `RootStart`,
`ChildStart`, `OneShotStart`, `OneShotChildStart`, `Cancellation`, `Suspension`,
or `Terminal` field. The returned `StorageActivityResult` must have exactly the
matching field. The runtime uses `storage.ContractError` to tell an engine that
the same command cannot succeed on retry. `runtime.WithStorageActivityTimeout`
sets the activity's Start-to-Close timeout; it must be greater than zero.

Custom engines apply the shared request and result contract instead of
recreating the shipped adapters' validation:

```go
import "goa.design/goa-ai/runtime/agent/engine/contract"

func (e *Engine) StartWorkflow(ctx context.Context, request engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
    normalized, err := contract.NormalizeRootRequest(request)
    if err != nil {
        return nil, err
    }

    // Retain normalized.Digest with normalized.Request.ID while this execution
    // can be queried. Return the existing handle only when an exact retry has
    // the same digest.
    return e.start(ctx, normalized.Request, normalized.Digest)
}
```

Call `contract.NormalizeChildRequest` before retaining or submitting a child
request. Keep each normalized request private. Call `contract.CopyRunInput`
before every initial or retry handler attempt. After success, retain one private
result from `contract.CopyRunOutput`, then call `CopyRunOutput` again for every
wait, query, or other caller-facing read. The shared normalization converts
search values to their portable types and fixes the root request digest. Each
adapter still translates and submits those values through its own backend.
The public functions use only `engine` and `api` values.

Custom adapters must also implement `RegisterAgentChildActivity` and
`ExecuteAgentChildActivity`. This activity prepares a nested agent's messages,
renders any consumer-side prompt, and returns exactly one `Success` or
`Failure`. `Success` contains only the messages and prompt render facts that
workflow history must retain. The workflow derives the child run, session,
parent, tool, and label identity from the original recorded tool call. Workflow
code must not read prompt storage directly because replay could otherwise see a
newer prompt version than the original execution.

The in-memory adapter copies child activity input and output and every
successful root or child workflow output through the same type-preserving,
1 MiB converter used at the Temporal workflow boundary. Returned workflow
outputs are independent of the handler-owned value. Tests that pass in memory
therefore do not hide a retry, serialization, ownership, or payload-size failure
that Temporal would surface.

`StartChildWorkflow` returns only after an engine accepts or rejects the child
start. The Temporal adapter waits for Temporal's child-start acknowledgement;
the returned handle represents the already-accepted child's later completion.

`QueryRunCompletion` returns the current `Status` in the same `RunCompletion`
value as the terminal `Output` or `WorkflowError`. Open runs return only their
non-terminal status. Closed runs return their terminal status, stable
`CompletedAt` time, and output or workflow error. The method's separate `error`
reports that the adapter could not retrieve this information. There is no
separate status method.
Adapters must preserve this distinction so completion recovery cannot record a
network or backend query error as the workflow's own failure.

The activity methods and `Await` no longer accept a separate
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
        TaskQueue: "assistant",
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

### Registry and model-request traces

Every registry replica emits `toolregistry.catalog.entry` for each active
toolset returned by the shared catalog. Retired records remain stored but do
not produce this span. Its `toolregistry.registry` and
`toolregistry.toolset` attributes identify the registry and toolset. This span
records catalog membership only; it does not report whether a provider is
ready.

Applications may list required names in `Registry.Config.ExpectedToolsets`.
After each successful catalog read, every replica emits
`toolregistry.catalog.expectation` for every configured name.
`toolregistry.present` is `1` when the active catalog contains the name and `0`
when it does not. A failed catalog read emits no expectation result, so an
application can alert on observed absence without treating missing telemetry
as proof that a toolset is missing. This list observes application requirements
only; it does not change registration, routing, or call admission.

After recording the catalog entry, registry replicas compete for an expiring
Redis lease. The replica that wins emits `toolregistry.health` for that toolset
and ping interval. Its attributes are:

- `toolregistry.registry` and `toolregistry.toolset` identify the registry and
  toolset.
- `toolregistry.ready` is `1` only when at least one provider lease can accept
  calls and a pong for the current provider set is still fresh. It is `0`
  otherwise.
- `toolregistry.provider_count` is the number of provider leases that can
  accept calls.
- `toolregistry.pong_seen` says whether the current provider set has returned
  a pong. `toolregistry.last_pong_age_ms` is present only after one has been
  seen, and `toolregistry.staleness_threshold_ms` states when it becomes stale.

A missing `toolregistry.catalog.entry` does not prove absence because the
catalog read or trace delivery may have failed. Use an observed expectation
span with `toolregistry.present=0` when the application configures required
names. A missing `toolregistry.health` span does not mean the provider is unready because
another replica may have won the lease. Each registry replica emits
`toolregistry.health.sweep` for its scheduler attempt. Revision, catalog
enumeration, and lease errors are recorded on that span with
`toolregistry.step`; a toolset name is included when the failing step had
already selected one. A health span records catalog-read and ping-publication
errors on itself.

These spans use the application's configured OpenTelemetry sampling policy.
An application that alerts when scheduler or catalog spans are absent must
always sample the `toolregistry.health.sweep` root. Its catalog-entry and
health child spans then inherit the same recorded decision.

Each planner model-call span records `goa_ai.request.tool_count` and
`goa_ai.request.tool_names`. These fields show the exact catalog advertised to
that request. They contain names only; tool arguments and run labels are not
recorded.

---

## Feature Modules

| Package | Purpose |
|---------|---------|
| `features/memory/mongo` | MongoDB-backed memory store |
| `features/prompt/mongo` | MongoDB-backed prompt override store |
| `features/stream/pulse` | Pulse message bus sink |
| `features/model/bedrock` | AWS Bedrock model client |
| `features/model/openai` | OpenAI-compatible model client |
| `features/model/anthropic` | Direct Anthropic Claude API client |
| `features/model/gateway` | Remote model gateway client |
| `features/model/middleware` | Rate limiting, logging, metrics |
| `features/policy/basic` | Basic policy engine |

---

## MCP Callers

The `runtime/mcp` package provides callers for stdio and HTTP MCP servers. Both
callers perform the MCP initialize handshake when they are created and require
the application's name and version.

### StdioCaller

Spawns an MCP server as a subprocess and communicates via stdin/stdout:

```go
import "goa.design/goa-ai/runtime/mcp"

caller, err := mcp.NewStdioCaller(ctx, mcp.StdioOptions{
    Command: "npx",
    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
    Env:     []string{"HOME=" + os.Getenv("HOME")},
    ClientInfo: mcp.ClientInfo{
        Name:    "my-agent",
        Version: "1.0.0",
    },
})
if err != nil {
    log.Fatal(err)
}
defer caller.Close()
```

### HTTPCaller

Sends JSON-RPC requests to an MCP HTTP endpoint. The server may return each
response as JSON or as an HTTP event stream; the same caller handles both
formats.

```go
caller, err := mcp.NewHTTPCaller(ctx, mcp.HTTPOptions{
    Endpoint: "https://mcp-server.example.com/mcp",
    ClientInfo: mcp.ClientInfo{
        Name:    "my-agent",
        Version: "1.0.0",
    },
})
if err != nil {
    log.Fatal(err)
}
```

Both callers implement the `mcp.Caller` interface. They return typed transport,
protocol, malformed-response, and tool-execution errors without retrying or
turning error text into control flow. Generated MCP executors classify those
errors into the canonical `planner.ToolFailure` contract.

```go
type Caller interface {
    CallTool(ctx context.Context, req CallRequest) (CallResponse, error)
}

type CallRequest struct {
    Tool    string
    Payload json.RawMessage
}

type CallResponse struct {
    Content           []string
    StructuredContent json.RawMessage
}
```

A Goa-generated MCP client exposes the same caller contract:

```go
caller, err := mcpservice.NewCaller(ctx, client, mcp.ClientInfo{
    Name:    "my-agent",
    Version: "1.0.0",
})
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
        Action: planner.RecoveryCorrectCall,
        Issues: validationErr.Issues(),
    },
}
```

Executors do not set `PriorInput` or `ExampleJSON`. Activities may return a
`correct_call` failure without those fields. The workflow then verifies that
the call came from a model tool use and overwrites both fields from its retained
`ToolCall` and registered `ToolSpec` before recording the result.

### Validation Issues and Tool Failures

The model-output checks described in
[Model-Visible Tool Arguments](#model-visible-tool-arguments) happen before a
tool is scheduled. A qualifying rejection produces limited-size guidance for a
replacement model call without including rejected argument values; ordinary
decoder and internal errors are terminal.

A tool can separately fail after execution begins because a bound service
method rejects a domain value. Generated providers convert supported Goa
validation errors into `[]tools.FieldIssue`, and registry executors preserve
those issues in `planner.ToolFailure`. The workflow supplies the original
model-authored input and generated example only after matching the saved tool
call ID. This execution-time path does not change which model-response decoder
errors qualify for correction.

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

rl := mdlmw.NewOutputReservationAdaptiveRateLimiter(
    ctx,
    throughputMap,     // *rmap.Map for cluster-wide state (nil for local)
    "bedrock:sonnet",  // Model family key
    80_000,            // Initial quota-token capacity per minute
    1_000_000,         // Maximum quota-token capacity per minute
)

limitedClient, err := rl.Middleware()(modelClient)
if err != nil {
    return err
}
if err := rt.RegisterModel("bedrock", limitedClient); err != nil {
    return err
}
```

The input-only constructor reserves the provider's exact input-token count. The
output-reservation constructor adds `Request.MaxTokens`, so the bucket uses the
same units a provider deducts when the request starts. The constructors use
different cluster keys because their stored capacity values have different
units. Both forms probe upward after a unary response or successful stream end,
and back off after a unary or streaming rate-limit error.

---

## Common Patterns

### Bootstrap Helper

Generated `goa example` emits `cmd/<service>/agents_bootstrap.go`:

```go
// The host provides the runtime store; bootstrap wires the runtime and agents.
rt, cleanup, err := bootstrap.New(ctx, runtimeStore)
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
    runtimeStore,
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
    ErrAgentNotFound         = errors.New("agent not found")
    ErrEngineNotConfigured   = errors.New("runtime engine not configured")
    ErrInvalidConfig         = errors.New("invalid configuration")
    ErrMissingSessionID      = errors.New("session id is required")
    ErrRunSuspensionCorrupt  = errors.New("run suspension corrupt")
    ErrRunSuspensionNotReady = errors.New("run suspension not ready")
    ErrWorkflowStartFailed   = errors.New("workflow start failed")
    ErrRegistrationClosed    = errors.New("registration closed after first run")
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
`unavailable` ProviderError when the provider event source closes before any
assistant message starts. Detect it with
`errors.Is(err, model.ErrEmptyStream)` and retry the request a bounded number
of times before surfacing the failure. A terminal provider event that arrives
without the required start event is malformed output instead; built-in
adapters return `OutputValidationError` with
`OutputValidationStreamProtocol`. Provider/network errors and caller
cancellation remain outside `OutputValidationError`.

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

6. **Configure stores for production.** Supply one durable host implementation
   of `storage.Store` for runtime lifecycle and records. Configure memory and
   prompt stores separately when those features need persistence.

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
