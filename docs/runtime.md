# Goa-AI Runtime Reference

This document explains how to bootstrap the goa-ai runtime, how the generated artifacts plug into it, and how the runtime coordinates engines, planners, tools, memory, hooks, and feature modules. Use it alongside `docs/plan.md` (architecture roadmap) and `docs/dsl.md` (design-time DSL).

## Quick Start

```go
package main

import (
    "context"

    "go.temporal.io/sdk/client"

    chat "example.com/assistant/gen/orchestrator/agents/chat"
    runtimeTemporal "goa.design/goa-ai/agents/runtime/engine/temporal"
    "goa.design/goa-ai/agents/runtime/planner"
    "goa.design/goa-ai/agents/runtime/runtime"
)

func main() {
    temporalEng, err := runtimeTemporal.New(runtimeTemporal.Options{
        ClientOptions: &client.Options{
            HostPort:  "127.0.0.1:7233",
            Namespace: "default",
        },
        WorkerOptions: runtimeTemporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
    })
    if err != nil {
        panic(err)
    }
    defer temporalEng.Close()

    rt := runtime.New(runtime.Options{Engine: temporalEng})

    if err := chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{Planner: newChatPlanner()}); err != nil {
        panic(err)
    }

    handle, err := rt.StartRun(context.Background(), runtime.RunInput{
        AgentID: "orchestrator.chat",
        RunID:   "session-1-run-1",
        SessionID: "session-1",
        Messages: []planner.AgentMessage{{Role: "user", Content: "Summarize the latest status."}},
        WorkflowOptions: &runtime.WorkflowOptions{
            Memo: map[string]any{"workflow_name": "ChatWorkflow", "session_id": "session-1"},
            SearchAttributes: map[string]any{"SessionID": "session-1"},
        },
    })
    if err != nil {
        panic(err)
    }

    var output runtime.RunOutput
    if err := handle.Wait(context.Background(), &output); err != nil {
        panic(err)
    }
}
```

* Generated `Register<Agent>` functions register workflows, activities, toolsets, codecs, and planner bindings.
* `WorkflowOptions` on `RunInput` map directly to engine start options (memo, search attributes, queue overrides), so services can attach Temporal metadata for observability.
* Application code wires engine/storage/policy/model feature modules into `runtime.Options`. The Temporal adapter auto-starts workers the first time a workflow runs; call `temporalEng.Worker().Start()` only when you need explicit lifecycle control.
* `StartRun` returns an engine workflow handle so callers can wait, signal, or cancel. `Run` simply calls `StartRun` and `Wait` for request/response transports.

## Pause & Resume

Human-in-loop workflows can suspend and resume runs using the runtime’s interrupt helpers. Behind the scenes, pause/resume signals update the run store and emit `run_paused`/`run_resumed` hook events so UI layers stay in sync.

```go
import "goa.design/goa-ai/agents/runtime/interrupt"

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

## Hooks, Memory, and Streaming

The runtime publishes structured events to a hook bus. Default subscribers include:

- **Memory subscriber** – writes tool calls, planner notes, assistant responses to the configured `memory.Store`.
- **Stream subscriber** – forwards events to the configured `stream.Sink` (Pulse by default). Services supply their own Pulse client via `features/stream/pulse.NewSink`.

Custom subscribers can register via `Hooks.Register` to emit analytics, trigger approval workflows, etc.

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
```

Services keep direct access to their Pulse client to create/close per-turn streams as needed, while the runtime handles event fan-out.

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

### Wiring Pulse Streaming

Use `features/stream/pulse` helpers to keep publishing and subscribing on the
same Pulse client. `NewRuntimeStreams` builds a sink for `runtime.Options.Stream`
and spawns subscribers (typically feeding SSE gateways) without duplicating
Redis plumbing:

```go
redisClient := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR")})
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
