# Goa Agents Framework – Planning Notes

## Goals

- Unify Goa design, code generation, and runtime patterns for distributed AI agents.
- Treat Strands-style agents and AURA-style workflow orchestration as first-class, code-generated abstractions.
- Subsume the existing tools plugin and goa-ai project with a single cohesive framework.
- Embrace a `Run`-centric API (instead of explicit Start/Resume) with a context object that drives creation vs. continuation.

The outcome is a Goa plugin plus runtime package that:

1. lets teams author agent/tool contracts declaratively,
2. emits strongly typed registries, Temporal scaffolding, and tool codecs, and
3. supplies a runtime library to execute the durable loop (plan → execute tools → resume) automatically.

## DSL Overview

All definitions live in the Goa design via a new plugin (working name: `goa.design/plugins/vX/agents`).

```go
import . "goa.design/plugins/vX/agents/dsl"

Service("atlas", func() {
    Agent("chat", func() {
        Description("Conversational front door for operators.")

        Tools(func() { // toolsets this agent consumes
            UseToolset("atlas.read")
            UseToolset("todos.manage")
        })

        RunPolicy(func() {
            DefaultCaps(
                MaxToolCalls(12),
                MaxConsecutiveFailedToolCalls(1),
            )
            TimeBudget("60s")
            InterruptsAllowed(true)
        })

        Workflow(func() {
            TaskQueue("chat-agent-tq")
            RetryPolicy(MaxAttempts(5), InitialInterval("5s"), Backoff("2x"))
        })
    })
})
```

Toolsets are declared either globally or within agent exports. For example:

```go
Toolset("atlas.read", func() {
    Description("Read-only Atlas data access")
    ExecModeActivity("atlas-data-tq")
    Tool("ListDevices", func() {
        Payload(func() { /* ... */ })
        Result(func() { /* ... */ })
    })
    Tool("ListAlarms", func() { /* ... */ })
})
```

### Key DSL Concepts

- **Agent**: Declares a Strands-style agent surface (model settings, tool imports, caps, workflow metadata).
- **Tools block**: Lists toolsets the agent depends on via `UseToolset("<id>")`. Toolsets may be declared globally or exported by other agents; the plugin wires registries, codecs, and execution policies accordingly.
- **Exports block**: (Optional) Declares toolsets that this agent publishes via `Toolset(...)`, allowing agents to wrap their own internal logic as reusable tools.
- **RunPolicy**: Caps/time budgets, interrupt support, default planner options. These compile into both agent runtime settings and workflow logic.
- **Workflow**: Temporal bindings (task queue, retry policy) used by generated scaffolding.
- **Toolset**: Standalone DSL construct describing payload/result schemas, validation, execution policy, and metadata for a group of tools.

The plugin infers Start vs. Resume based on `RunContext` data, so the DSL never has to mention those terms explicitly.

## Generated Artifacts

Running `goa gen` after adding the DSL produces:

1. **Design additions** (`design/<service>_agent.go`):
   - Service `Agent` with method `Run(ctx context.Context, payload *RunPayload) (*RunResult, error)`.
   - Typed `RunPayload` (`RunContext`, `Messages`, optional streaming flags).
   - Typed `RunResult` (final message, tool_results, etc.).
   - Shared types: `AgentMessage`, `ToolCall`, `ToolResult`, `RunContext`, `RetryHint`.

2. **Agent packages** under `gen/<service>/agents/<agent>/` with:
   - `agent.go`: constructor returning a configured Strands-compatible `AgentRunner`.
   - `config.go`: strongly typed config struct (model options, caps, tool binding).
   - `workflow.go`: Temporal workflow and activity stubs (`RunWorkflow`, `PlanActivity`, `ResumeActivity`, `ExecuteToolActivity`), already wiring the runtime package.
   - `registry.go`: registration helpers (planner registry, tool registries, executor overrides).
   - `tool_specs/` subdir: per-tool codecs, JSON schemas, and `ToolSpec` definitions (Strands-style).
   - Optional `agenttools/` helpers when the agent itself publishes tools for reuse by other agents.

3. **Runtime glue**:
   - Generated `RegisterAgent<AgentName>` function that:
     - registers model provider factories,
     - registers tool executors/specs in bulk,
     - registers Temporal workflows/activities with the runtime package.
   - Optional HTTP/gRPC handlers for `Run` (standard Goa output).

4. **Examples/tests** (optional flag): sample main showing how to wire inference gateway, Temporal client, streaming/persistence connectors.

## Runtime Package Composition

The runtime lives in a reusable Go module (`goa.design/plugins/vX/agents/runtime`). Generated code composes with it as follows:

```go
// main.go skeleton
rt := agentsruntime.NewRuntime(agentsruntime.Options{
    TemporalClient: temporalClient,
    Stream: streamSink,            // optional (Pulse-style)
    SessionStore: sessionStore,    // optional
    Policy: policyEngine,          // optional custom allowlist logic
})

// Register generated agent (sets up planners, tool registries, workflows)
atlasagents.RegisterChatAgent(rt,
    atlasagents.ChatAgentConfig{
        Model: bedrock.NewClient(...),
        AtlasDataClient: atlasdata.NewClient(...),
        TodosClient: todos.NewClient(...),
    },
)

// Start Goa service exposing Agent.Run
server := atlasserver.New(rt.RunHandler("chat"))
```

### Runtime Responsibilities

The runtime implements the durable loop, closely matching AURA’s orchestration:

1. **Run workflow**:
   - Inspect `RunContext` (run ID, resumption tokens, caps, retry hints).
   - Compute allowed tool list via generated registries + optional policy engine.
   - Invoke `Plan` (start) via generated agent wrapper (calls model provider).
   - While tool calls remain:
     - Publish tool_call event (optional streaming, persistence).
     - For each tool call, schedule `ExecuteToolActivity` on configured queue (respecting DSL’s execution mode).
       - Activities use generated codecs/clients to call underlying Goa service.
     - Publish tool_result events and persist session events.
     - Derive retry hints / adjust caps; re-run `Plan` (resume) with results.
   - Finalize conversation (assistant message), publish completion event.

2. **Automatic retries**:
   - Planner activities: DSL-defined retry policy.
   - Tool activities: DSL-defined execution mode (per-toolset queue) plus default exponential backoff.

3. **Interrupts & structured output**:
   - Generated agent adapters translate interrupts into workflow markers so human-in-the-loop resumes are natural.
   - Structured output requests (Pydantic-like) use generated schemas for validation and final result formatting.

4. **Telemetry & tracing**:
   - Integrated with clue; runtime emits spans for plan/run/execute plus tool schema metadata.

5. **Policy engine**:
   - Optional user-provided implementation. Receives context (caps, retry hints, candidate tools) and returns final allowlist.
   - Defaults to union of toolsets declared for the agent.

### Agent-as-tool adapters

Some agents (for example, an ADA-style planner) need to expose their own capabilities as reusable tools. The DSL captures this with an `Exports` block inside the agent definition, separate from the tools the agent consumes:

```go
Agent("atlas_data_agent", func() {
    Exports(func() {
        Toolset("atlas.read", func() {
            Tool("GetKeyEvents", func() {
                HelperPrompt("You are the ADA key-events planner...")
                Tags("ada", "read")
            })
            Tool("AnalyzeSensorPatterns", func() {
                HelperPrompt("You plan sensor analytics using AD queries...")
            })
        })
    })
})
```

Generation results:

- In `gen/<service>/agents/<agent>/agenttools/<toolset>/`, helper constructors and wrappers turn each published tool into a callable function other agents can import.
- Registration helpers (e.g., `RegisterAtlasReadTools(rt, cfg)`) fetch those helpers, register their tool specs, and expose them via the runtime so upstream agents can include them in their allowlists.
- The runtime pipeline stays the same: when a planner selects `ada_get_key_events`, the generated adapter runs the helper logic, then the generic tool executor persists evidence and returns the normalized result.

## Composition Story

Putting everything together:

1. **Design**: Author Goa service + agent DSL. `goa gen` produces design scaffolding, agent registries, Temporal stubs, and tool codecs/specs.
2. **Build**: Implement planner logic, register toolset clients, instantiate runtime with required adapters (model providers, service clients).
3. **Deploy**: Run Temporal worker (using generated registration) and Goa server with `Run` endpoint.
4. **Extend**: Add more tools or agents by updating the design and regenerating; runtime picks up new specs. Advanced features (multi-agent graphs, knowledge agents) can be layered as separate DSL additions sharing the same runtime.

## Immediate Work Plan

1. **Plugin scaffolding**:
   - Define DSL structures (`AgentExpr`, `ToolsExpr`, `RunPolicyExpr`, etc.).
   - Emit design types (`RunPayload`, `RunResult`, `AgentMessage`, etc.).
   - Generate tool specs/codecs (reusing/refining current tools plugin templates).

2. **Runtime integration**:
   - Adapt `Agent` to accept generated `RunConfig`.
   - Implement generic Temporal workflow + activities using generated metadata.
   - Expose registration hooks for planners, toolsets, policy, and streaming/persistence adapters.

3. **Example + docs**:
   - Build sample repo mirroring AURA chat+ADA loop end-to-end.
   - Document generated APIs and runtime wiring (update README, quickstart), including agent-as-tool adapters.

> **Note:** When implementing code or documentation generated from this plan, avoid referencing Strands or AURA directly. Use neutral terminology (e.g., “durable agent workflow,” “inspiration from existing agent architectures”) to keep goa-ai independent of private projects.

This staged approach keeps scope manageable while delivering a coherent agentic framework that formalizes the patterns already proven in Strands and AURA.
