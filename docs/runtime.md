# Goa-AI Runtime Reference

This document explains how to bootstrap the goa-ai runtime, how the generated artifacts plug into it, and how the runtime coordinates engines, planners, tools, memory, hooks, and feature modules. Use it alongside `docs/plan.md` (architecture roadmap) and `docs/dsl.md` (design-time DSL).

## Quick Start

```go
package main

import (
    "context"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
)

func main() {
    // In-memory engine is the default; pass Engine for Temporal or custom engines.
    rt := runtime.New()

    if err := chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{Planner: newChatPlanner()}); err != nil {
        panic(err)
    }

    client := chat.NewClient(rt)
    handle, err := client.Start(context.Background(), []planner.AgentMessage{{Role: "user", Content: "Summarize the latest status."}},
        runtime.WithSessionID("session-1"),
        runtime.WithMemo(map[string]any{"workflow_name": "ChatWorkflow", "session_id": "session-1"}),
        runtime.WithSearchAttributes(map[string]any{"SessionID": "session-1"}),
        runtime.WithTaskQueue("orchestrator.chat"),
    )
    if err != nil {
        panic(err)
    }

    var output *runtime.RunOutput
    if err := handle.Wait(context.Background(), &output); err != nil {
        panic(err)
    }
}
```

## Client-Only vs Worker

Two roles use the runtime:

- Client-only (submit runs): constructs a runtime with a client-capable engine and does not register agents. Use the generated `<agent>.NewClient(rt)` which carries the route (workflow + queue) registered by remote workers.
- Worker (execute runs): constructs a runtime with a worker-capable engine, registers agents (with real planners), and lets the engine poll and execute workflows/activities.

Client-only example:

```go
rt := runtime.New(runtime.Options{Engine: temporalClient}) // engine client

// No agent registration needed in a caller-only process
client := chat.NewClient(rt)
out, err := client.Run(ctx, msgs, runtime.WithSessionID("s1"))
```

Worker example:

```go
rt := runtime.New(runtime.Options{Engine: temporalWorker}) // worker-enabled engine
if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: myPlanner}); err != nil {
    panic(err)
}
// Start engine worker loop per engine’s integration (e.g., Temporal worker.Run())
```

* Generated `Register<Agent>` functions register workflows, activities, toolsets, codecs, and planner bindings.
* `AgentClient` exposes `Run` and `Start` bound to a given agent and accepts functional RunOptions to configure memo, search attributes, and queue.
* Application code wires engine/storage/policy/model feature modules into `runtime.Options`.
* Workers (per agent): When using a polling engine (e.g., Temporal), the runtime starts a worker for each registered agent by default. You rarely need to configure workers explicitly; production can rely on defaults. To override the queue for a specific agent or disable local workers entirely, see “Workers (per agent)”.
* Client-only callers: Processes that only start runs do not need to register agents locally. Use the generated `<agent>.NewClient(rt)` which carries the route (workflow + queue) to remote workers.
* `Start` returns an engine workflow handle so callers can wait, signal, or cancel. `Run` simply calls `Start` and `Wait` for request/response transports.
* DSL-generated Goa types expose `ConvertTo*` / `CreateFrom*` helpers (backed by `agents/apitypes`) so transports can bridge into the runtime/planner structs without hand-written mappers; rely on those conversions in service handlers and tests rather than re-implementing translation logic.

### Advanced & Dynamic

Use advanced entry points when you need dynamic selection across many agents or
to construct a client using a route explicitly.

```go
// Dynamic across locally registered agents
c, _ := rt.Client(agent.Ident("service.chat"))
out, _ := c.Run(ctx, msgs, runtime.WithSessionID("s1"))

// Explicit route (caller-only), no local registration required
route := runtime.AgentRoute{ID: agent.Ident("service.chat"), WorkflowName: "ChatWorkflow", DefaultTaskQueue: "orchestrator.chat"}
c2 := rt.MustClientFor(route)
out2, _ := c2.Run(ctx, msgs, runtime.WithSessionID("s2"))
```

Engines and transports can also integrate directly with the workflow handler
and activity handlers for advanced use cases. See `runtime/agent/runtime` package
docs for `WorkflowHandler`, `PlanStartActivityHandler`, `PlanResumeActivityHandler`,
and `ExecuteToolActivityHandler`.

### Run Contracts

- `SessionID` is required at run start. `Start` fails fast when `SessionID` is empty or whitespace.
- Agents must be registered before the first run. The runtime rejects registration after the first run submission with `ErrRegistrationClosed` to keep engine workers deterministic.
- Tool executors receive explicit per‑call metadata (`ToolCallMeta`) rather than fishing values from `context.Context`.
- Do not rely on implicit fallbacks; all domain identifiers (run, session, turn, correlation) must be passed explicitly.

## Pause & Resume

Human-in-loop workflows can suspend and resume runs using the runtime’s interrupt helpers. Behind the scenes, pause/resume signals update the run store and emit `run_paused`/`run_resumed` hook events so UI layers stay in sync.

```go
import "goa.design/goa-ai/runtime/agent/interrupt"

// Pause
if err := rt.PauseRun(ctx, interrupt.PauseRequest{RunID: "session-1-run-1", Reason: "human_review"}); err != nil {
    panic(err)
}

// Resume
if err := rt.ResumeRun(ctx, interrupt.ResumeRequest{RunID: "session-1-run-1"}); err != nil {
    panic(err)
}
```

## Architecture Overview

| Layer | Responsibility |
| --- | --- |
| DSL + Codegen | Produce agent registries, tool specs/codecs, workflows, MCP adapters |
| Runtime Core | Orchestrates plan/start/resume loop, policy enforcement, hooks, memory |
| Workflow Engine Adapter | Temporal adapter implements `engine.Engine`; other engines can plug in |
| Feature Modules | Optional integrations (MCP, Pulse, Mongo stores, model providers) |

## Workers (per agent)

For engines that poll task queues (e.g., Temporal), the runtime manages one worker per agent. By default, you do not need to configure workers: the runtime binds each agent to its generated queue and starts a worker in this process. This is suitable for most production setups.

Customize only when needed:

- Override queue for an agent (rare):

```go
// Build a worker config for the agent package
w := chat.NewWorker(runtime.WithQueue("orchestrator.chat"))

// Supply the worker to the runtime at construction
rt := runtime.New(
    runtime.WithEngine(temporalEngine),
    runtime.WithWorker(chat.AgentID, w),
)

// Register the agent as usual
_ = chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: myPlanner})
```

- Submit-only process (no local workers):

```go
rt := runtime.New(runtime.Options{Engine: temporalEngine})

// No registration needed in a submit-only caller
client := chat.NewClient(rt)
out, _ := client.Run(ctx, msgs, runtime.WithSessionID("s1"))
```

Notes:
- In-memory engine: workers are unsupported and ignored; all runs execute inline.
- Workers are supplied via runtime options at construction time; attaching workers later is not supported.
- If you don’t supply a worker for an agent, the runtime starts a default worker for that agent (when the engine supports polling). For submit-only processes, disable worker auto-start using engine-level options (e.g., Temporal’s DisableWorkerAutoStart) and rely on remote workers.

## Hooks, Memory, and Streaming

The runtime publishes structured events to a hook bus. Default subscribers include:

- **Memory subscriber** – writes tool calls, planner notes, assistant responses to the configured `memory.Store`.
- **Stream subscriber** – forwards events to the configured `stream.Sink` (Pulse by default). Services supply their own Pulse client via `features/stream/pulse.NewSink`.

Custom subscribers can register via `Hooks.Register` to emit analytics, trigger approval workflows, etc.

Streaming event mapping (default StreamSubscriber):

- ToolCallScheduled → `tool_start` (payload: `*hooks.ToolCallScheduledEvent`)
- ToolResultReceived → `tool_end` (payload: `*hooks.ToolResultReceivedEvent`)
- PlannerNote → `planner_thought` (payload: `string`)
- AssistantMessage → `assistant_reply` (payload: `string`)
- ToolCallUpdated → `tool_update` (payload: `*hooks.ToolCallUpdatedEvent`)

Flow overview:

```
hooks.Bus → hooks.StreamSubscriber.HandleEvent → stream.Sink.Send → transport (SSE/WebSocket/Pulse) → client
```

Naming note: only the sink exposes `Send`. The subscriber receives hook events and
forwards them by calling `sink.Send` under the hood.

The `stream` package exposes a small interface `Event` implemented by concrete types:

- `AssistantReply{Run, Text}`
- `PlannerThought{Run, Note}`
- `ToolStart{Run, Data: ToolStartPayload{...}}`
- `ToolEnd{Run, Data: ToolEndPayload{...}}`

Transports should type-switch on `stream.Event` for compile-time safety:

```go
switch e := evt.(type) {
case stream.AssistantReply:
    // e.Text
case stream.ToolStart:
    // e.Data.ToolCallID, e.Data.ToolName, e.Data.Payload
case stream.ToolUpdate:
    // e.Data.ExpectedChildrenTotal, e.Data.ToolCallID
case stream.ToolEnd:
    // e.Data.Result, e.Data.Error
}
```

## Calling Exported Tools (Agent-as-Tool)

When an agent exports tools, Goa-AI generates an `agenttools` package with typed
helpers to build planner tool requests. Import the agenttools package and its
specs package to construct typed payloads and keep planners clean and type-safe.
Per‑toolset specs packages also export typed tool identifiers (tools.Ident) for
all generated tools — including non‑exported toolsets. Prefer these constants
over ad‑hoc strings.

```go
import (
    chattools "example.com/assistant/gen/orchestrator/agents/chat/agenttools/search"
)

// Build a typed tool call for the exported Search tool
req := chattools.NewSearchCall(&chattools.SearchPayload{
    Query: "golang",
    Limit: 5,
}, chattools.WithToolCallID("tc-1"))

// Use in a planner result
result := planner.PlanResult{ToolCalls: []planner.ToolRequest{req}}
```

## Executor-First Tool Execution

Generated service toolsets expose a single, generic constructor:

- `New<Agent><Toolset>ToolsetRegistration(exec runtime.ToolCallExecutor)`

Applications register an executor implementation for each consumed toolset. The executor decides how to run the tool (service client, MCP, nested agent, etc.) and receives explicit per-call metadata via `ToolCallMeta` (RunID, SessionID, TurnID, ToolCallID, ParentToolCallID).

To reduce mapping boilerplate, Goa generates per-tool transforms when shapes are compatible:

- `ToMethodPayload_<Tool>(in <ToolArgs>) (<MethodPayload>, error)`
- `ToToolReturn_<Tool>(in <MethodResult>) (<ToolReturn>, error)`

Transforms are emitted under `gen/<service>/agents/<agent>/specs/<toolset>/transforms.go` and use Goa’s GoTransform to safely map fields. If a transform isn’t emitted, write an explicit mapper in the executor.

Method vs. Tool Result and the Execution Envelope

- Method result: domain type returned by your service method.
- Tool result: planner-facing subset type (from tool specs).
- Execution envelope (server-only): runtime record with code, summary, data JSON, evidence refs, backend calls, duration, error, retry hint, and facts.

Executor Guidance

- Use generated transforms when present; otherwise implement clear field mappings.
- Keep server-only fields out of tool results; envelope/telemetry capture them.
- MCP-backed toolsets typically wrap generated clients with `mcpruntime.Caller` in the executor.

For convenience, services often translate:

- `tool_start` → a “tool_call” chunk (ID, name, payload) for SSE/WebSocket
- `tool_end` → a “tool_result” chunk (ID, result, error)

Typed streaming example (per-request sink):

```go
// Attach a temporary subscriber for this request/connection.
sub, _ := streambridge.Register(rt.Bus, mySink)
defer sub.Close()

// In your sink implementation, handle typed events.
func (s *mySSE) Send(ctx context.Context, evt stream.Event) error {
    switch e := evt.(type) {
    case stream.ToolStart:
        log.Printf("tool start: %s (%s)", e.Data.ToolName, e.Data.ToolCallID)
    case stream.ToolUpdate:
        log.Printf("tool update: %s expected children now %d", e.Data.ToolCallID, e.Data.ExpectedChildrenTotal)
    case stream.ToolEnd:
        if e.Data.Error != nil {
            log.Printf("tool end (error): %s err=%v", e.Data.ToolName, e.Data.Error)
        } else {
            log.Printf("tool end: %s result=%v", e.Data.ToolName, e.Data.Result)
        }
    case stream.PlannerThought:
        log.Printf("planner: %s", e.Note)
    case stream.AssistantReply:
        log.Printf("assistant: %s", e.Text)
    }
    return nil
}
```

## Workflow Options & Metadata

Use `runtime.WorkflowOptions` to forward memo/search attributes to the engine:

```go
runInput.WorkflowOptions = &runtime.WorkflowOptions{
    Memo: map[string]any{"workflow_name": "ChatWorkflow"},
    SearchAttributes: map[string]any{"SessionID": runInput.SessionID},
    TaskQueue: "custom.queue",          // optional override
    RetryPolicy: engine.RetryPolicy{MaxAttempts: 3},
}
```

The runtime mirrors these options into `engine.WorkflowStartRequest`. Engines that support memo/search attributes (e.g., Temporal) persist them for later queries and dashboards.

## Policy & Labels

`policy.Engine` implementations receive `policy.Input` (tool metadata, retry hints, labels) every turn. Returned labels merge into `run.Context` and the stored run record, so label updates remain visible in subsequent planner calls and audit logs.

## Pulse Streams

To publish hook events to Pulse:

```go
pulseClient := pulse.NewClient(redisClient)
sink, _ := pulseSink.NewSink(pulseSink.Options{Client: pulseClient})
rt := runtime.New(runtime.Options{Engine: eng, Stream: sink})

API shortcuts

- Helper constructors: `stream.NewAssistantReply`, `NewPlannerThought`, `NewToolStart`, `NewToolEnd` create typed events with base metadata set.
- Bridge helpers: `agents/runtime/stream/bridge` exposes `Register(bus, sink)` and `NewSubscriber(sink)` so you can wire the hook bus to any `stream.Sink` without importing the hooks subscriber directly.

## Introspection & Subscriptions

List registered agents and tool metadata at runtime (prefer generated constants):

```go
import (
    chattools "example.com/assistant/gen/orchestrator/agents/chat/agenttools/search"
)

agents := rt.ListAgents()     // []agent.Ident
toolsets := rt.ListToolsets() // []string

spec, ok := rt.ToolSpec(chattools.Search)
schemas, ok := rt.ToolSchema(chattools.Search)
specs := rt.ToolSpecsForAgent(chat.AgentID)
```

Subscribe to a single run’s stream using a filtered subscriber (returns a close func):

```go
type mySink struct {}
func (s *mySink) Send(ctx context.Context, e stream.Event) error { /* deliver */ return nil }
func (s *mySink) Close(ctx context.Context) error { return nil }

stop, err := rt.SubscribeRun(ctx, "run-123", &mySink{})
if err != nil { panic(err) }
defer stop()
```

Runtime publishes hook events internally; the subscription bridges only relevant
events (assistant replies, planner thoughts, tool start/update/end) for the given
run ID into the provided sink.

## Policy Overrides (Runtime)

Adjust an agent’s policy at runtime (local to the current process) for experiments
or temporary backoffs. Only non-zero fields are applied (and `InterruptsAllowed`
when true). Overrides apply to subsequent runs.

```go
// Reduce caps and allow interruptions for the chat agent
_ = rt.OverridePolicy(chat.AgentID, runtime.RunPolicy{
    MaxToolCalls:                    3,
    MaxConsecutiveFailedToolCalls:   1,
    InterruptsAllowed:               true,
})
```
```

Services keep direct access to their Pulse client to create/close per-turn streams as needed, while the runtime handles event fan-out.

## Schemas and Summaries

Prefer generated tool specs over documentation introspection:

- Schema lookup: use `gen/<svc>/agents/<agent>/<toolset>/tool_specs.Specs`. Each
  entry exposes `Payload.Schema` and `Result.Schema` plus typed/untyped codecs.
  Example:

```go
spec := tool_specs.Specs[i] // or find by name
payloadSchema := spec.Payload.Schema
resultSchema := spec.Result.Schema
bytes, _ := spec.Payload.Codec.Marshal(v) // JSON encode typed payload
```

- Summaries: use runtime/planner data instead of schema text. Two common
  patterns:
  - Streaming: accumulate a `planner.StreamSummary` via `planner.ConsumeStream`
    and surface `summary.Text`.
  - Typed results: derive concise summaries from concrete result fields inside
    adapters or service logic (e.g., count items, key fields). Avoid
    reconstructing summaries from JSON Schemas.

Do not parse or traverse `docs.json` at runtime; all information required for
tool execution and validation is available in the generated specs and codecs.

## Memory & Session Stores

`memory.Store` and `run.Store` have in-memory references plus Mongo-backed implementations (`features/memory/mongo`, `features/run/mongo`). Feature modules follow the client pattern (domain-specific client packages with Clue-generated mocks) so services can swap storage backends easily.

## Planner Contract

Planners implement:

```go
type Planner interface {
    PlanStart(ctx context.Context, input planner.PlanInput) (planner.PlanResult, error)
    PlanResume(ctx context.Context, input planner.PlanResumeInput) (planner.PlanResult, error)
}
```

`PlanResult` contains tool calls, final response, annotations, and optional `RetryHint`. The runtime enforces caps, schedules tool activities, and feeds tool results back into `PlanResume` until a final response is produced.

### Determinism & Correlation

- ToolCall IDs: planners may supply `ToolRequest.ToolCallID` (e.g., from a model `tool_call.id`). The runtime preserves it end-to-end and returns the same ID in `planner.ToolResult.ToolCallID`. When omitted, the runtime assigns a deterministic ID derived from `(runID, turnID, toolName, index)` for replay safety.
- Time: workflows use a deterministic clock via `engine.WorkflowContext.Now()` (Temporal → `workflow.Now`). Deadlines and durations are computed from this source; avoid `time.Now()` in workflow code.

## Feature Modules

- `features/mcp/*` – MCP suite DSL/codegen/runtime callers (HTTP/SSE/stdio).
- `features/memory/mongo` – durable memory store.
- `features/run/mongo` – run metadata store + search repositories.
- `features/stream/pulse` – Pulse sink/subscriber helpers (users pass their Pulse client).
- `features/model/{bedrock,openai}` – model client adapters for planners.

Each module is optional; services import the ones they need and pass the resulting clients into `runtime.Options` or their planners.

## Model Clients & Streaming

- Register model providers (Bedrock, OpenAI, custom) by calling `rt.RegisterModel("provider-id", client)` before registering agents. Generated agent config structs expose a `Models` map so planners can select the desired client per turn.
- `model.Client` now exposes `Complete` (unary) and `Stream`. Set `model.Request.Stream = true` (and optionally `ThinkingOptions`) when planners want Bedrock-style streaming; call `Client.Stream` to receive incremental `model.Chunk`s (text/tool_call/thinking/usage/stop). Callers must drain the returned `model.Streamer` (loop on `Recv` until `io.EOF`) and invoke `Close` when finished.
- Bedrock adapter translates ConverseStream events into chunk types and automatically injects the beta thinking header when `ThinkingOptions.Enable` is true and tools remain available. OpenAI currently reports `model.ErrStreamingUnsupported`, so planners should fall back to `Complete` until streaming support lands.
- Streaming chunks flow through the runtime hook bus the same way unary responses do: the capture sink publishes partial assistant replies, tool-call updates emit `tool_call_scheduled`/`tool_result_received`, and Pulse subscribers can surface progress to clients without custom glue.
- `planner.AgentContext` exposes `EmitAssistantMessage`/`EmitPlannerNote` so streaming planners can forward chunks without touching the hook bus. Pair these with `planner.ConsumeStream(ctx, streamer, agentCtx)` to drain provider streamers, emit events, and receive a `StreamSummary` (final text + requested tool calls + usage) that can be converted into a `PlanResult`.

## Example Bootstrap Helpers

`goa example` emits `cmd/<service>/agents_bootstrap.go` when a design declares agents. The helper:

- Creates a Temporal engine + in-memory stores, then calls each generated `Register<Agent>` function.
- Instantiates planner stubs (`cmd/<service>/agents_planner_<agent>.go`) so examples compile out-of-the-box.
- Emits a `configure<Agent>MCPCallers` stub only when the agent uses `UseMCPToolset`. Replace the placeholder `mcpruntime.CallerFunc` entries with real callers (e.g., `mcpruntime.NewHTTPCaller`, `NewSSECaller`, or a custom adapter) before running agents in production. Services without MCP bindings avoid unused imports automatically.

If you implement a bespoke bootstrap path (e.g., non-Temporal engine, custom stores), you can delete the generated helper and wire everything manually by following the pattern above.

### Wiring Pulse Streaming

Use `features/stream/pulse` helpers to keep publishing and subscribing on the
same Pulse client. `NewRuntimeStreams` builds a sink for `runtime.Options.Stream`
and spawns subscribers (typically feeding SSE gateways) without duplicating
Redis plumbing:

```go
redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"}) // inject via flag/config in your app
pulseClient, _ := pulseclient.New(pulseclient.Options{Redis: redisClient})

streams, _ := pulsestream.NewRuntimeStreams(pulsestream.RuntimeStreamsOptions{
    Client: pulseClient,
})

rt := runtime.New(runtime.Options{
    Engine: temporalEngine,
    Stream: streams.Sink(),
    MemoryStore: memoryStore,
})

sub, _ := streams.NewSubscriber(pulsestream.SubscriberOptions{SinkName: "front"})
events, errs, cancel, _ := sub.Subscribe(ctx, "run/abc123")

defer cancel()
defer streams.Close(ctx)
```

`runtime.New` automatically registers the stream sink with the hook bus when the
`Stream` option is non-nil, so tool/planner/assistant events flow to Pulse with
no additional wiring.

## Glossary

- Tool Execution Record
  - A server-only structured record composed at execution time that packages outcome classification and observability metadata for storage/streaming: code, summary, data (JSON), evidence references, backend calls, duration, error, and facts. Generated adapters must not add server-only fields to tool results; instead the record is composed in the ExecuteTool activity path and persisted/streamed via hooks. It is never sent to the model.
