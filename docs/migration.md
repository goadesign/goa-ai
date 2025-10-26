# Migration Guide: Legacy goa-ai → Goa Agents Framework

This document walks existing goa-ai users (earlier MCP‑only plugin) through the
steps required to adopt the new agents framework described in `docs/plan.md`.
It assumes you are already comfortable with Goa service designs and the legacy
MCP generator.

## What Changed

| Legacy Concept | New Equivalent |
| --- | --- |
| `goa-ai` MCP plugin living under `goa.design/plugins/tools` | `agents/dsl` for design-time declarations, `agents/codegen` for generation, `agents/runtime` for execution |
| Hand-authored MCP server implementations | Generated agent packages + tool codecs + registries under `gen/<service>/agents/...` |
| Ad-hoc workflow loops | Durable workflows driven by `agents/runtime/runtime` atop the `engine` interface (Temporal adapter provided) |
| Manual JSON schema validators | Generated `tool_types.go` + `tool_codecs.go` mirroring Goa validations |
| Imperative tool registration | Declarative `Toolset`, `Uses`, `Exports`, and `UseMCPToolset` DSL blocks |

The migration path keeps Goa’s design-first workflow intact: you still define
services in `design/*.go`, run `goa gen`, and implement business logic in your
service packages. The difference is that agents, planners, toolsets, and
runtime plumbing are now first-class DSL constructs with shared runtime support.

## Step-by-Step Migration

### 1. Update Dependencies

1. Require the latest Goa (`goa.design/goa/v3`) and goa-ai modules.
2. Ensure Go 1.24+ and Temporal SDK `v1.37.0` (ships with the new OTEL contrib package).
3. Replace references to the old MCP plugin (`goa.design/plugins/tools`) with the new agents DSL import:
   ```go
   agentsdsl "goa.design/goa-ai/agents/dsl"
   ```.

### 2. Introduce the Agent DSL

Inside each Goa service definition:

```go
var _ = Service("orchestrator", func() {
    Description("Chat orchestration service")

    agentsdsl.Agent("orchestrator.chat", func() {
        Description("Chat agent that plans and executes tool calls")

        agentsdsl.Tools(func() {
            agentsdsl.UseToolset(myInternalTools)
            agentsdsl.UseMCPToolset("assistant.mcp", func() {
                Description("External MCP suite exported by the assistant service")
            })
        })

        agentsdsl.RunPolicy(func() {
            agentsdsl.MaxToolCalls(8)
            agentsdsl.TimeBudget("2m")
        })
    })
})
```

Key DSL additions:

- `Toolset`, `Tool`, `Arg`, `Return`, `HelperPrompt`, `Tags` describe native toolsets.
- `Uses`, `UseToolset`, `UseMCPToolset` reference internal or external tool suites.
- `Exports` wraps toolsets that should be exposed as tools (agent-as-tool).
- `RunPolicy`, `RetryPolicy`, `WorkflowOptions` configure planner caps and activity options.

Reference `docs/dsl.md` for the full API.

### 3. Regenerate Code

Run:
```bash
goa gen <module>/design
```

The generator now emits:

- `gen/<service>/agents/<agent>` packages (planner configs, workflow handlers, activities).
- Tool type/codec/spec packages mirroring Goa validations.
- Registration helpers (`Register<Service><Agent>Agent`, `Register<Service><Suite>Toolset`).
- `features/mcp` helpers if you declare MCP suites.

Do **not** edit files in `gen/`; re-run `goa gen` after DSL changes.

### 4. Implement Planners & Prompt Providers

Each agent config produces a `Config` struct:

```go
chatCfg := chat.ChatAgentConfig{
    Planner: &chatPlanner{ /* ... */ },
    PromptProvider: chat.NewPromptProvider(),
}
```

Implement the `planner.Planner` interface (`PlanStart` / `PlanResume`) in your service
packages and pass it through the generated config when registering the agent.

### 5. Wire the Runtime & Workflow Engine

Instantiate the runtime once per process:

```go
temporalEng, err := temporal.New(temporal.Options{
    ClientOptions: &client.Options{HostPort: "...", Namespace: "..."},
    WorkerOptions: temporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
})
if err != nil { log.Fatal(err) }

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
```

`runtime.RegisterAgent` (via the generated helper) registers workflows, activities,
toolsets, and planner bindings with the runtime and engine:

```go
if err := chat.RegisterChatAgent(ctx, rt, chatCfg); err != nil {
    log.Fatal(err)
}
```

Workflows are launched via `rt.Run` or `rt.StartRun`.

### 6. Configure Feature Modules

- **Memory/session:** use `features/memory/mongo` and `features/run/mongo` for durable
  transcripts and run state.
- **Stream sink:** use `features/stream/pulse` with your Redis/Pulse client to fan out
  runtime events (tool call scheduled/result, assistant reply, retry hints).
- **Models:** register Bedrock/OpenAI clients via `features/model/{bedrock,openai}` and
  look them up through `planner.AgentContext.ModelClient`.
- **Policy:** opt into `features/policy/basic` or supply your own `policy.Engine`.
- **MCP:** generated adapters live under `features/mcp`; register them with
  `UseMCPToolset` and call `Register<Service><Suite>Toolset`.

### 7. Update Transports & CLI Paths

Legacy MCP servers often instantiated tool registries manually. Replace that boilerplate
with the generated helpers:

```go
caller := mcpruntime.NewHTTPCaller(...)
if err := mcpassistant.RegisterAssistantAssistantMcpToolset(ctx, rt, caller); err != nil {
    log.Fatal(err)
}
```

HTTP/CLI transports continue to call `runtime.Run`, but now benefit from the shared
runtime features (memory persistence, policy enforcement, telemetry).

### 8. Integration Tests

- Regenerate the example tree before running integration tests (`goa gen && goa example`).
- Use the runtime harness (`example/runtime_harness.go`) for deterministic planner/tool tests.
- Update YAML scenarios under `integration_tests/scenarios` to expect the new hook events
  (snake_case fields: `auto_initialize`, `client_mode`, `stream_expect`, `expect_retry`).

### 9. Verification Checklist

- [ ] All agents defined via `agentsdsl.Agent` inside Goa services.
- [ ] `goa gen` produces `gen/<service>/agents/...` packages with no manual edits.
- [ ] Planners implement `planner.Planner` and are registered in `Register<Agent>`.
- [ ] Runtime initialized once with Temporal engine + Mongo stores + Pulse sink.
- [ ] Toolsets (native + MCP) registered via generated helpers.
- [ ] Policy/model/memory/stream feature modules wired through runtime options.
- [ ] `go test ./...` (including integration suite) passes.
- [ ] Operational dashboards updated to consume hook events (`hooks` bus publishes the same JSON envelopes as Pulse).

## Additional References

- `docs/dsl.md` – full DSL reference with examples.
- `docs/runtime.md` – runtime wiring, hook events, telemetry guidance.
- `example/` – chat data loop showcasing MCP toolsets, planner, runtime harness.
- `agents/runtime/engine/temporal` – Temporal adapter options and telemetry behavior.

Following the steps above keeps existing business logic intact while adopting the
new abstractions that make durable agent workflows, tool retries, and observability
consistent across services.
