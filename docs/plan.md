# Goa Agents Framework – Planning Notes

## Introduction & Rationale

Goa-ai is evolving from a narrow MCP codegen tool into a complete agentic platform. The goals of this plan are:

- **Codify agent architecture**: capture proven patterns from AURA (durable workflows, Start/Resume loops, strict contracts) and from emerging agent frameworks (e.g., memory, tool registries, planner interfaces) in a reusable Go toolkit.
- **Leverage Goa strengths**: use Goa’s design-first philosophy so agents, toolsets, and workflows are all declared in DSL and automatically generate the rich scaffolding (codecs, registries, telemetry hooks) teams need.
- **Decouple infrastructure choices**: keep workflow engines, memory stores, and model providers behind clean interfaces so Temporal + MongoDB + Bedrock are defaults but not hard dependencies.
- **Modularise features**: house optional integrations (MCP, retries, CLI) under `features/`, making it easy to opt-in/opt-out without bloating the core runtime.
- **Improve developer productivity**: provide consistent runtime semantics (policy enforcement, streaming, telemetry) so teams focus on planner/business logic and not the plumbing.

Desired outcomes:

- A cohesive DSL that defines agents, toolsets, policies, and workflows, replacing the old `goa-ai` snippets and the standalone tools plugin.
- A runtime package that implements the durable execution loop, memory persistence, hook fan-out, and planner interfaces with Temporal + MongoDB defaults and plug-in points for alternatives.
- Feature modules (starting with MCP) that layer capabilities on top of the core runtime without coupling.
- Clear documentation, examples, and migration guidance so existing goa-ai users can adopt the new framework with minimal friction.

The sections below break down DSL concepts, generated artifacts, planner contracts, runtime packages, and the implementation roadmap.

## Goals

- Unify Goa design, code generation, and runtime patterns for distributed AI agents.
- Treat Strands-style agents and AURA-style workflow orchestration as first-class, code-generated abstractions.
- Subsume the existing tools plugin and goa-ai project with a single cohesive framework.
- Embrace a `Run`-centric API (instead of explicit Start/Resume) with a context object that drives creation vs. continuation.

The outcome is a modular framework composed of:

1. **Design + codegen**: a DSL for agents, toolsets, and workflows with generated registries, codecs, and scaffolding (implemented via a Goa plugin for convenience but not required).
2. **Runtime package**: durable execution kernel (Temporal adapter provided; other engines can implement the same interface) handling plan/execute/resume loops, policy, telemetry, streaming, and memory.
3. **Feature modules**: optional capabilities (e.g., MCP integration, retries) packaged separately so the core remains lightweight.

## Current Status

- ✅ Repository restructure landed: new `agents/` (DSL, codegen, runtime) and `features/` hierarchy exists, with the legacy MCP plugin staged for migration.
- ✅ DSL + expressions implemented with tests; agent/toolset/run-policy constructs match the spec and feed the new generator data model.
- ✅ Tool specs pipeline mirrors the legacy tools plugin: generated `tool_types.go`, `tool_codecs.go`, and `tool_spec.go` now emit strong types, JSON codecs, and registries consumed by the runtime.
- ✅ Runtime scaffolding includes the durable loop, policy/caps enforcement, hook bus, and codec-aware memory persistence; execute-tool calls now route through Temporal activities via `WorkflowContext.ExecuteActivity`.
- ✅ Runtime scaffolding includes the durable loop, policy/caps enforcement, hook bus, codec-aware memory persistence, session tracking, and telemetry-aware logging; execute-tool calls now route through Temporal activities via `WorkflowContext.ExecuteActivity` and hook events feed stream/memory subscribers.
- ✅ Mongo-backed memory store shipped under `features/memory/mongo`, giving the runtime a durable default that mirrors the expected Temporal + Mongo deployment model.
- ✅ Mongo-backed session store shipped under `features/session/mongo`, matching the `session.Store` contract for durable run metadata.
- ✅ Mongo client packages now live under `features/(memory|session)/mongo/clients/mongo`, mirroring the `clients/mongo` pattern from existing systems and generating Clue mocks automatically (via cmg) so unit tests can stub Mongo behavior without bespoke fakes.
- ✅ Pulse-backed stream sink shipped under `features/stream/pulse`: accepts a caller-provided Redis client, mirrors the Pulse layering pattern, and exposes clue-generated mocks for deterministic tests.
- ✅ Model + policy feature adapters shipped: `features/model/bedrock`, `features/model/openai`, and `features/policy/basic` wire runtime.ModelClient/policy.Engine contracts to real providers so services can opt in without bespoke glue.
- ✅ MCP runtime callers now cover HTTP, SSE, and stdio transports with OTEL context propagation and unit tests validating trace injection plus structured payload handling. JSON-RPC error codes map into structured retry hints so policy engines receive precise reasons.
- ✅ Goa example generation now emits `handleHTTPServer` invocations in the same order as the helper signature; the agents plugin no longer rewrites the example main, and we carry a temporary `replace goa.design/goa/v3 => ../goa` until the upstream release lands.
- ✅ Temporal engine adapter now instantiates clients (when requested), wires OTEL tracing/metrics interceptors, auto-starts workers on the first run (while keeping `WorkerController` for advanced lifecycle control), supports multiple registered workflows, rehydrates Clue/OTEL context inside activities, and exposes a `Close` helper when it owns the client.
- ✅ Planner activities (`Plan`, `Resume`) now run as real runtime activities. Codegen registers them with DSL-derived retry/timeout options, `Runtime.ExecuteWorkflow` schedules them through `runPlanActivity`, and unit tests cover both PlanStart/PlanResume paths (while future signal/interrupt hooks remain on the roadmap).
- ✅ Example “chat data loop” harness (planner + runtime bootstrap) runs end-to-end via `RuntimeHarness`, registers the generated MCP toolset helper, and now shares the same MCP adapter shim used by the JSON-RPC server so `go test ./...` (integration suite included) passes.
- ✅ Migration guide (`docs/migration.md`) documents the step-by-step path from the legacy goa-ai MCP plugin to the new agents DSL/runtime stack, covering dependency updates, DSL adoption, runtime wiring, feature modules, and verification checklists.
- ⏳ Runtime subpackages (session store, policy/planner/model adapters, stream, telemetry) are defined but need the remaining feature modules finalized (e.g., MCP bridge, advanced policy engines, knowledge adapters).
- ⏳ Documentation rollout: refresh the root README, add scenario walkthroughs (agent-as-tool, MCP suite opt-in), and expand integration tests so the published guides stay in lockstep with the codebase.
- ⏳ AURA parity track: interrupts/signal plumbing, child-tracker driven nested agents, session search/diagnostics APIs, and policy override hooks are still pending (see “Required Enhancements for AURA Parity” below).

## Alignment With Existing Systems

The current AURA implementation (Temporal workflows, Pulse streaming, Mongo-backed session store, multiple Goa services) is the immediate consumer of goa-ai. The design below keeps the following compatibilities front and center:

- **Workflow parity**: Generated workflows/activities (Run → Plan/Resume → Tool execution) mirror the existing orchestrator patterns (Temporal workflows launched via `services/orchestrator`, activities implemented in agent services) so components like `orchestrator`, `chat-agent`, and `atlas-data-agent` can be ported without rewriting business logic. `RunInput/RunOutput` match the payloads exchanged today (session IDs, labels, tool traces) so callers such as `front`, `todos`, or Temporal workers have a straight migration path.
- **Pulse streaming & hooks**: The runtime hook bus maps directly to Pulse topics (`workflow_started`, `tool_call_scheduled`, etc.) so the existing `front` SSE fan-out can subscribe with minimal changes.
- **Session + Mongo**: `memory.Store` and `session.Store` interfaces reflect the current Mongo-backed stores, preserving invariants like durable audit trails and resumable conversations.
- **Todos & additional agents**: Feature modules can register bespoke toolsets (e.g., `todos` Temporal worker pattern) or planner adapters just like the current `todos` service; agents acting as tools (ADA, diagnostics, remediation) are supported via the `Exports` DSL and generated helper packages.
- **Inference engine integration**: Planners retrieve a `ModelClient` from the runtime (mirroring the centralized `inference-engine` gateway) so existing prompt/trace tooling can be reused.
- **Policy / caps**: The `policy.Engine` contract captures the retry/cap logic already enforced by the orchestrator (max tool calls, consecutive failures, human-in-loop interrupts).
- **Planner-tool separation (ADA pattern)**: Tool schemas defined in Goa should mirror ADA’s pattern—tools exist for LLM allowlists but lack transports. Orchestrator-generated workflows resume the planner and fan out to actual executors (e.g., `atlas-data`) through Temporal activities. The DSL’s `Exports` plus generated helper packages replicate ADA’s “agent as tool provider” semantics while letting the orchestrator remain the single tool executor.
- **Temporal worker topology**: The runtime must produce registration helpers akin to `services/orchestrator/cmd/orchestrator/main.go` (Temporal client, worker registration, downstream Goa clients). Generated `RegisterAgent` functions need clear extension points for wiring existing worker binaries.

> **Note:** These alignment points serve as validation that goa-ai can host the existing AURA use cases, but the framework itself remains vendor-neutral. All DSL/runtime/codegen artifacts must stick to generic terminology (agent, toolset, workflow, policy) so that other systems can adopt goa-ai without inheriting AURA-specific naming or behaviors. Feature modules (e.g., an ADA-style agent package) will live under `features/` and translate domain jargon at the edges, keeping the core abstractions clean and reusable.

This ensures porting services from `~/src/aura` is a mechanical exercise: Goa designs move over to the new DSL, Temporal workers register through goa-ai runtime helpers, and Pulse/Mongo/Bedrock integrations land in feature modules without bespoke glue.

## DSL Overview

All definitions live in the Goa design via a new plugin (working name: `goa.design/plugins/vX/agents`). The DSL produces code that drives durable workflows (Temporal by default) and orchestrates tool execution. Model provider configuration remains an app-level concern—teams register their preferred `ModelClient` during runtime bootstrap.

```go
import . "goa.design/plugins/vX/agents/dsl"

Service("atlas", func() {
    Agent("chat", "Conversational front door for operators.", func() {
        Uses(func() {
            Toolset("atlas.read", func() {
                Tool("list_devices", "List devices", func() {
                    Args(func() { /* ... */ })
                    Return(func() { /* ... */ })
                })
            })
        })

        RunPolicy(func() {
            DefaultCaps(
                MaxToolCalls(12),
                MaxConsecutiveFailedToolCalls(3),
            )
            TimeBudget("60s")
            InterruptsAllowed(true)
        })
    })
})
```

Toolsets are declared either globally or within agent exports. Global toolsets provide shared capabilities, while `Exports` blocks let an agent publish helper tools that other agents can depend on. Each `Toolset` contains one or more `Tool` definitions with `Args`, `Return`, `Tags`, and other metadata. Code generation emits strongly typed helpers keyed by the design-time names so the runtime never fabricates new identifiers.

### Key DSL Concepts

- **Agent**: Declares a Strands-style agent surface. Signature: `Agent(name string, description string, dsl func())`.
- **Uses**: Declares toolsets the agent consumes. Each `Toolset` call inside `Uses` adds a dependency; global toolsets may also be referenced by code.
- **Exports**: (Optional) Declares toolsets that this agent publishes. The generated code exposes helpers so other agents can call these exported tools.
- **RunPolicy**: Configures caps, time budgets, and interrupt settings enforced at runtime.
- **Toolset / Tool**: Standalone DSL constructs describing payload/result schemas, validation, and metadata for a group of tools. Each tool has a name, description, and `Args`/`Return` definitions; generated code emits codecs/specs automatically. Task queue names for tool execution are derived by convention (e.g., `service_agent_toolset`) to keep designs declarative.
- **RunContext**: Generated input structure containing run-level metadata (run ID, labels, optional resume tokens). Passed to planners and workflows; runtime augments it with counters, retry hints, and other execution state between turns.

Workflow engine configuration (task queues, retry policy, etc.) is handled when wiring the runtime; the DSL focuses on contract and capability declarations.

The plugin infers Start vs. Resume based on `RunContext` data, so the DSL never has to mention those terms explicitly.

## Generated Artifacts

Running `goa gen` after adding the DSL produces:

1. **Goa service artifacts** (derived from the DSL during `goa gen`, no files written under `design/`):
   - A logical `Run` method (or dedicated agent service) with associated payload/result types (`RunPayload`, `RunResult`, `AgentMessage`, `ToolCall`, `ToolResult`, `RunContext`, `RetryHint`, etc.).
   - JSON Schema/OpenAPI metadata for tool payloads/results and agent inputs so standard Goa transports and docs retain full fidelity.

2. **Agent packages** under `gen/<service>/agents/<agent>/` with:
   - `agent.go`: constructor returning a configured Strands-compatible `AgentRunner`.
   - `config.go`: strongly typed config struct (model options, caps, tool binding).
   - `workflow.go`: Temporal workflow and activity stubs (`RunWorkflow`, `PlanActivity`, `ResumeActivity`, `ExecuteToolActivity`), already wiring the runtime package.
   - `registry.go`: registration helpers (planner registry, tool registries, executor overrides).
   - `tool_specs/` subdir: per-tool codecs, JSON schemas, and `ToolSpec` definitions (Strands-style).
   - Optional `agenttools/` helpers when the agent itself publishes tools for reuse by other agents.

3. **Runtime glue (generated wiring)**:
   - Generated `RegisterAgent<AgentName>` function that:
     - registers model provider factories,
     - registers tool executors/specs in bulk,
     - registers the agent workflow/activities with the runtime package.
   - Optional HTTP/gRPC handlers for `Run` (standard Goa output).

4. **Examples/tests** (via `goa example`): sample main showing how to wire inference gateway, Temporal client, streaming/persistence connectors. The JSON-RPC CLI patcher now operates generically—rather than hardcoding tool names, we rewrite the generated `doJSONRPC` section via Goa’s `codegen.File` APIs and a small template so every MCP-enabled service automatically exposes its tool commands through the adapter endpoints.

## Code Generation Summary

Running `goa gen` after adding agents to a design produces cohesive outputs across three layers. Each subsection below outlines what lands where and why.

### Design layer (`design/*.go`)

- Adds the agent-facing service interface with the `Run` endpoint plus typed payload/result structures (`RunPayload`, `RunResult`, `AgentMessage`, `ToolCall`, `ToolResult`, `RunContext`, `RetryHint`, etc.).
- Embeds schema/validation logic for every `Toolset` and `Tool` so OpenAPI/JSON Schema stay accurate and downstream tooling (e.g., UI forms) can rely on the generated types.

### Generated agent packages (`gen/<service>/agents/<agent>/…`)

- `agent.go`, `config.go`: constructors and strongly typed configuration for planners, model clients, and tool clients. These files mostly bind design-specific types to library-level interfaces (`agents/runtime/planner`, `agents/runtime/model`).
- `workflow.go`: durable workflow and activity handlers (`RunWorkflow`, `PlanActivity`, `ExecuteToolActivity`, etc.). Only the business-specific bits (agent identifiers, toolset names, planner calls) are generated; shared helper logic lives in `agents/runtime`.
- `registry.go`: helpers (e.g., `RegisterAgent<AgentName>`) that hook the generated logic into `agents/runtime`. This file is thin, referencing library helpers for registering workflows, tool specs, and planners.
- `tool_specs/`: codecs, JSON schemas, and `ToolSpec` definitions for every tool declared in the DSL (consumed or exported). These are the most design-dependent since payload/result schemas vary per tool.
- `agenttools/<toolset>/`: emitted only when an agent exports toolsets; contains reusable Go helpers so other agents can call those tools. The generated code is small and defers to shared adapters in `agents/runtime/toolexec`.

### Runtime glue

- Generated registration functions tie the design-time declarations into the runtime: register workflows/activities with the `WorkflowEngine`, register planners, and wire tool executors.
- Optional Goa transport scaffolding exposes the agent’s `Run` method over HTTP/gRPC for interoperability.

This layered approach keeps the DSL, generated scaffolding, and runtime wiring aligned without hand-written glue.

## Code Generation Data Structures

Goa’s generator model is expression → data structure → template. The agent plugin follows the same pattern:

### Expression extraction

- `expr.AgentExpr` gathered from the DSL (one per agent declaration).
- `expr.ToolsetExpr` + nested `expr.ToolExpr` for both consumed and exported toolsets.
- Supporting expressions (`RunPolicyExpr`, `CapsExpr`, etc.).

### Intermediary data types

For each agent we build a Go struct that captures everything templates need without making them walk the raw expression tree. Think of them as the agent equivalent of Goa’s `service.Data`. The core structs are:

**`AgentData` (one per agent)**
- `Name`, `Description`, `ID`, `ServiceName` – direct DSL metadata and service scope used across files.
- `GoName`, `PackageName`, `PathName`, `ImportPath`, `Dir` – naming helpers for generating package/import paths and filesystem locations.
- `ConfigType`, `StructName`, `VarName` – precomputed identifiers for the generated config struct, agent wrapper type, and common receiver/variable names.
- `Workflow` block: `WorkflowFunc`, `WorkflowDefinitionVar`, `WorkflowName`, `WorkflowQueue` – derived workflow identifiers/task queue names.
- `RunPolicy` (`RunPolicyData`) – resolved caps/time budgets used by runtime templates.
- Toolset/view helpers: `ConsumedToolsets`, `ExportedToolsets`, `AllToolsets`, `Tools`.
- `ToolSpecsPackage`, `ToolSpecsImportPath`, `ToolSpecsDir` – directories for tool spec packages.
- Future runtime glue: references to planner/model wiring, default hook labels, and workflow/activity definitions (see `RuntimeData` below).

**`ToolsetData` (one per toolset)**
- `Name`, `Description`, `ServiceName` (blank for global), `QualifiedName` (`service.toolset`).
- `Kind` (consumed/exported/global) so templates know whether to emit helper packages.
- `TaskQueue` – sanitized queue name for activities (defaults to `<service>_<agent>_<toolset>_tasks`).
- `Agent` – backlink to parent `AgentData` (nil for global toolsets) plus `PathName`, `PackageName`, `PackageImportPath`, `Dir` for helper packages.
- Export-specific helpers: `AgentToolsPackage`, `AgentToolsImport`, `AgentToolsDir` describing where agent-as-tool helpers live.
- `Tools` – slice of `ToolData` for the toolset.

**`ToolData` (one per tool)**
- `Name`, `Description`, `Tags` – DSL metadata.
- `QualifiedName` (`service.toolset.tool`) and `DisplayName` (`toolset.tool`) for registries/UI.
- Goa attribute pointers for `Args` / `Return` so template can feed them into existing Goa helpers (codecs, JSON Schema) and generate the JSON marshaler/unmarshaler logic previously provided by the `plugins/tools` package.
- Optional prompt/template metadata (future addition once DSL exposes helper prompts).

**`RuntimeData` / workflow descriptors**
- Derived structures describing the workflow + activities we must register: definitions (names, handler references, default queues), activity-specific queues/timeouts, and identifiers for tool executor registration. These feed the `workflow.go`, `activities.go`, and `registry.go` templates without forcing them to recompute names.
- `WorkflowArtifact` captures the handler function name, exported definition variable, logical name, and queue for the workflow.
- `ActivityArtifact` captures the handler function name, exported definition variable, logical name, queue, and (eventually) retry/timeout settings for each generated activity (`Plan`, `Resume`, `ExecuteTool`, ...).
  - Planner-facing activities default to a conservative retry policy (3 attempts, 1s initial backoff, coefficient 2) and a 2-minute timeout, mirroring today’s orchestrator defaults. These values are stored on each `ActivityArtifact` so templates can emit both the registration-time `ActivityDefinition` options and the per-invocation `ActivityRequest` overrides that the runtime applies when it schedules `PlanStart` / `PlanResume`.

These structs are populated once during the plugin’s prepare phase and handed to templates.

### Templates

We follow Goa’s generator conventions so we benefit from `codegen.File` helpers. Planned templates (all under `agents/codegen/templates/`) include:

- `agent.go.tmpl`: emits the design-specific constructor/wrapper; references shared helpers from `agents/runtime/runtime`.
- `config.go.tmpl`: produces the typed config struct and validation glue (mostly mechanical).
- `workflow.go.tmpl`: generates the workflow entry point that delegates to runtime helpers (only agent IDs, planner invocations, and toolset routing differ per design).
- `activities.go.tmpl`: emits the activity handlers (`PlanActivity`, `ResumeActivity`, `ExecuteToolActivity`, etc.) and their `engine.ActivityDefinition` declarations so registries can register them with the runtime.
- `registry.go.tmpl`: emits lightweight registration helpers (`RegisterAgent<AgentName>`, exported tool registries) that call into shared `agents/runtime` registration APIs.
- `tool_types.go.tmpl`: emits the strongly typed payload/result structs for every tool declaration (only when the DSL defined inline attributes). Types reference existing user types when possible and embed doc comments for clarity.
- `tool_codecs.go.tmpl`: mirrors the legacy tools plugin by generating JSON marshal/unmarshal helpers, per-type validation wrappers, and both typed plus untyped (`tools.JSONCodec`) codecs. It also exposes helper functions (`PayloadCodec`, `ResultCodec`) keyed by the fully qualified tool name.
- `tool_spec.go.tmpl`: builds the `tools.ToolSpec` registry that records metadata, JSON schemas, and codec references for each tool. Policy adapters reuse the generated `policy.ToolMetadata` slice, and helper functions (`Spec`, `Names`, `PayloadSchema`, `ResultSchema`) provide runtime lookup.
- `agenttools.go.tmpl`: generated only for exported toolsets; wraps shared tool-execution adapters with design-specific payload/result bindings.

Anything common across agents lives in library packages (e.g., `agents/runtime/*`, `agents/runtime/toolexec`). Templates only emit the glue that varies per design, ensuring reuse and keeping diffs manageable.

This approach keeps the codegen layer predictable: expressions stay in `agents/expr`, data structs bridge to templates, and final Go files are generated by Goa’s standard pipeline.

## Runtime Package Composition

The runtime lives in `agents/runtime`. Generated code and user code compose with it as follows:

```go
// main.go skeleton
rt := agentsruntime.NewRuntime(agentsruntime.Options{
    TemporalClient: temporalClient,
    Stream: streamSink,            // optional (Pulse-backed stream publisher)
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

The runtime implements the durable loop using a durable workflow engine (Temporal integration provided by default), closely matching AURA’s orchestration:

1. **Run workflow**:
   - Inspect `RunContext` (run ID, resumption tokens, caps, retry hints).
   - Compute allowed tool list via generated registries + optional policy engine.
- Invoke `Plan` (start) via generated agent wrapper (calls model provider).
  - Planner invocations run inside `PlanActivity` / `ResumeActivity` so Temporal can scale them independently of tool execution.
- While tool calls remain:
- Publish tool_call event (optional streaming, persistence).
- For each tool call, schedule `ExecuteToolActivity` on the ActivityQueue declared by the toolset.
  - The runtime marshals tool payloads/results using the generated `tool_specs` codecs so Temporal activities can run out-of-process while preserving schema validation.
  - Activities use generated codecs/clients to call underlying Goa service.
- Publish tool_result events and persist session events.
- Stream planner/tool/assistant updates through the configured `stream.Sink` (default Pulse/SSE bridge) so callers can receive live progress, just like AURA’s Pulse topics (`workflow.tool_call`, `workflow.tool_result`, `workflow.assistant_chunk`).
     - Derive retry hints / adjust caps; re-run `Plan` (resume) with results.
   - Finalize conversation (assistant message), publish completion event.

2. **Automatic retries**:
   - Planner activities: DSL-defined retry policy.
   - Tool activities: scheduled on the ActivityQueue declared for the toolset, with default exponential backoff.

3. **Error propagation & structured output**:
   - When tool activities or planners return errors, the runtime emits error events (via hooks), logs telemetry, and uses `RetryHint` plus policy configuration to decide whether to retry, surface the error, or disable further tool use.
   - Generated agent adapters translate interrupts into workflow markers so human-in-the-loop resumes are natural.
   - Structured output requests (Pydantic-like) use generated schemas for validation and final result formatting.

4. **Telemetry & tracing**:
   - Integrated with Clue (OpenTelemetry-based) instrumentation; the runtime emits spans for plan/run/execute plus tool schema metadata.
5. **Policy + caps enforcement**:
   - Each agent registers a `RunPolicy` defining time budgets and tool caps. The runtime enforces these limits and invokes the optional `policy.Engine` each turn so dynamic allowlists and retry hints can adjust caps or disable tools altogether.
6. **Memory + session persistence**:
   - Tool calls/results, assistant messages, and planner notes are appended to the configured `memory.Store`; session metadata is pushed to `session.Store` so resumptions and audit trails stay consistent. Hook events mirror the Pulse topics used today, keeping SSE streaming intact.
   - The runtime updates `session.Store` on every state transition (pending → running → success/failure) mirroring AURA’s session service so downstream Pulse subscribers and dashboards share a consistent view of active runs.

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

## Required Enhancements for AURA Parity

The table below breaks down what must exist in goa-ai vs. what the AURA ports will provide when migrating each subsystem.

| Area | Needed from goa-ai | Supplied by AURA (via feature modules/subscribers) |
| --- | --- | --- |
| **Workflow start + metadata** | Extend `runtime.RunInput` with optional workflow options (memo/search attributes, task queue overrides) that `engine.StartWorkflow` forwards to Temporal. Expose workflow IDs/run IDs in `RunStartedEvent` so services can register related workflows (knowledge/lifecycle) when a run begins. | Orchestrator registers hook subscribers to create/close knowledge workflows and to mutate memo/search attributes before calling `StartRun`. |
| **Pulse streams** | Keep `stream.Sink` generic and ship `features/stream/pulse.NewRuntimeStreams` (wraps sink + subscribers on a caller-provided Pulse client) so runtime hooks automatically fan out events. | Orchestrator passes its Pulse client into the helper and, if needed, manages per-turn stream creation/closure using its own client wrappers. |
| **Policy overrides & caps** | Already supported: `policy.Input` carries retry hints, tool metadata, and labels; `policy.Decision.Caps` merges into runtime caps and decisions are persisted via `PolicyDecisionEvent`. No further core changes needed beyond ensuring labels persist into the run store. | AURA supplies a richer `policy.Engine` implementation (caps overrides, approvals) and registers hook subscribers to audit policy decisions. |
| **Session search / diagnostics** | Provide `features/run/mongo/search` repositories + interfaces so runtime and subscribers can store/load run metadata. Allow arbitrary events via `memory.Store.AppendEvents` and hook subscribers. | AURA implements subscribers that serialize domain-specific events (operational hints, diagnostics) into its Mongo collections and wires custom search endpoints using the shared client pattern. |
| **Knowledge / policy modules** | Expose interfaces (`policy.Engine`, `model.Client`, `memory.Store`, `stream.Sink`) and keep generated code generic. No AURA-specific logic lands in core. | AURA packages its knowledge base, advanced policies, and model integrations as feature modules that implement those interfaces. |
| **Observability / Pulse schema** | Provide structured hook events (`ToolCallScheduled`, `ToolCallUpdated`, `PolicyDecision`, etc.) with enough metadata (turn IDs, child counts). Let hook subscribers transform them if needed. | AURA adapts its UI/stream processors (or writes a subscriber) to map goa-ai events into the exact JSON envelopes existing dashboards expect. |
| **Agent-as-tool & child tracking** | Already implemented: `childTracker`, `ToolCallUpdatedEvent`, agent inline execution, deterministic turn sequencing. No further core work required. | ADA planners run inside goa-ai using `ExecuteAgentInline`; orchestrator consumes the existing events to drive its progress UI. |
| **External worker fan-out** | Tool calls already return from planners; the framework is agnostic to how tool results arrive. | Orchestrator continues to route ADA tool calls to external workers and feeds the results back into the workflow before calling `Resume`. |

This separation keeps goa-ai generic while providing the extension points AURA needs to plug in its domain logic.

## Composition Story

Putting everything together:

1. **Design**: Author Goa service + agent DSL. `goa gen` produces design scaffolding, agent registries, Temporal stubs, and tool codecs/specs.
2. **Build**: Implement planner logic, register toolset clients, instantiate runtime with required adapters (model providers, service clients).
3. **Deploy**: Run Temporal worker (using generated registration) and Goa server with `Run` endpoint.
4. **Extend**: Add more tools or agents by updating the design and regenerating; runtime picks up new specs. Advanced features (multi-agent graphs, knowledge agents) can be layered as separate DSL additions or feature modules.

## Immediate Work Plan

### Completed
1. Repository restructure: new `agents/`, `agents/runtime/`, and `features/` trees are in place with the legacy MCP plugin staged inside `features/mcp` for migration.
2. DSL + codegen foundation: agent/toolset/run-policy DSL implemented with unit tests, generator data structs refactored, and tool spec/type/codec templates emitting runtime-consumable packages.
3. Runtime loop & tool activities: Execute-tool scheduling now runs through the workflow engine using codec-aware payloads, and generated registries feed `tool_specs.Specs` into the runtime at registration time.
4. Planner activities & workflow glue: Generated Plan/Resume activities call the runtime helpers, `ExecuteWorkflow` schedules them with DSL-derived retry/timeout options, unit tests assert both activity paths, and pause/resume signals now flow through the interrupt controller plus `PauseRun` / `ResumeRun` helpers so human-in-loop workflows can suspend deterministically.
5. API conversions & bootstrap polish: Shared Goa-facing structs live in `agents/apitypes`, the DSL wires `CreateFrom`/`ConvertTo` so Goa emits runtime/planner conversion helpers, and the example bootstrap helper now emits per-agent `configure<MCP>Callers` stubs only when MCP toolsets are referenced (no more unused imports for MCP-free services).

### In Progress / Next
1. **AURA parity & feature readiness**
   - Go-side work (✅ complete): `RunInput` carries workflow options, start hooks expose IDs, `features/stream/pulse.NewRuntimeStreams` wires Pulse sinks/subscribers to runtime.Options, and policy label overrides now persist into the run store.
   - AURA-side work: implement hook subscribers that start knowledge workflows, manage Pulse streams, and provide richer `policy.Engine`, `model.Client`, and session-search modules on top of goa-ai’s interfaces. Keep these in `features/` modules so other adopters can opt in.
   - Refit the legacy MCP plugin under `features/mcp` so MCP suites register through the new runtime hooks, add model streaming + thinking support (`model.Stream` + request options) so planners can mirror existing inference-engine flows, ship a documented example wiring `runtime.RegisterModel` → planner usage, and provide a general policy override hook (or API) so orchestrators can adjust caps mid-run. Temporal adapter already supports multiple workflows/activities; remaining wiring is handled by the host service.

2. **Runtime subpackages polish**
   - Finish any remaining gaps in the `policy`, `planner`, `stream`, and `telemetry` subpackages (Pulse fan-out, Clue instrumentation, blob adapters for large payloads) now that the AURA requirements are captured above. Mongo-backed memory and session stores already exist; remaining work is mostly wiring default subscribers/adapters so downstream services can opt in without bespoke code.

3. **API conversion follow-ups**
   - Audit remaining call sites (policy hooks, CLI adapters) to ensure everything routes through the generated apitype helpers before surfacing data outside the runtime.
   - Explore adding optional streaming chunk conversions once the runtime exposes chunk types via the DSL.

4. **Docs & rollout** (✅ core docs updated)
   - `docs/dsl.md`, `docs/runtime.md`, and the chat data loop walkthrough now cover streaming, planner usage, and runtime wiring. Remaining documentation work is limited to the README refresh once the last parity features land.
   - Expand integration/unit tests to cover README walkthroughs, ensuring the documented flows (registration, MCP usage, runtime harness) stay green.

> **Note:** When implementing code or documentation generated from this plan, avoid referencing Strands or AURA directly. Use neutral terminology (e.g., “durable agent workflow,” “inspiration from existing agent architectures”) to keep goa-ai independent of private projects.

This staged approach keeps scope manageable while delivering a coherent agentic framework that formalizes the patterns already proven in Strands and AURA.

## DSL Reference

All DSL symbols live in `goa.design/plugins/vX/agents/dsl`. Each function follows Goa’s style: call it inside `Service`, `Agent`, `Tools`, etc., to configure generated artefacts.

```go
// Agent(name string, dsl func())
// Declares an agent configuration under the current Goa Service block. Generates agent configuration,
// tool bindings, workflow scaffolding, and registries.

// Uses(dsl func())
// Adds toolset dependencies to the agent. Contains Toolset(...) blocks either referring to or inlining
// toolset definitions.

// Exports(dsl func())
// Declares toolsets that this agent publishes for other agents to consume. Contains Toolset(...)
// blocks either referring to or inlining toolset definitions.

// Toolset(name string, dsl func())
// Defines a named toolset (payload/result schema, execution policy, metadata). When declared
// at top-level or inside Uses/Exports, codegen emits payload/result types, JSON Schema, codecs,
// registry helpers, and an exported constant with the toolset name.

// Tool(name string, dsl func())
// Declares a single tool inside a Toolset. Inside, define Payload/Result schemas, helper prompts,
// tags, execution hints, etc.

// Args(dsl func()), Return(dsl func())
// Define the fields of the tool args/return value using the standard Goa attribute DSL.

// BindTo(name string) or BindTo(service string, method string)
// Links a tool to a Goa service method. One-arg form binds to a method on the current
// service; two-arg form binds to a method on another service in the design. Codegen
// generates a service-backed toolset registration that calls the target client method.
// Strong contracts: adapters are REQUIRED; no fallback coercion.
// Examples:
//   BindTo("GetKeyEvents")                  // current service
//   BindTo("atlas_data", "GetKeyEvents")    // cross-service

// HelperPrompt(template string)
// Attaches prompt guidance (as a Go text/template) to a tool exported by an agent. The template
// can reference fields from the tool payload/result and is rendered before invoking the helper
// logic inside agent-as-tool adapters.

// Tags(values ...string)
// Associates metadata tags with the tool (for policy/allowlist decisions).

// ActivityQueue(name string)
// Overrides the default durable-execution queue name for tools in this toolset. If omitted,
// the queue name defaults to a sanitized version of "<service>_<toolset>".

// RunPolicy(dsl func())
// Configures per-agent resource caps (MaxToolCalls, MaxConsecutiveFailedToolCalls, TimeBudget, etc.).

// MaxToolCalls(n int), MaxConsecutiveFailedToolCalls(n int), TimeBudget(duration string), InterruptsAllowed(bool)
// Convenience setters inside RunPolicy controlling tool-use caps and turn time budget.
```

## Workflow Engine Interface

To keep goa-ai runtime decoupled from any specific durable workflow backend, generated code and the runtime depend on a small `WorkflowEngine` interface. The default implementation (`temporal.Engine`) adapts Temporal to this interface, but other engines can plug in as long as they satisfy it.

```go
// WorkflowEngine abstracts registration and execution of workflows/activities.
type WorkflowEngine interface {
    RegisterWorkflow(ctx context.Context, def WorkflowDefinition) error
    RegisterActivity(ctx context.Context, def ActivityDefinition) error
    StartWorkflow(ctx context.Context, req WorkflowStartRequest) (WorkflowHandle, error)
}

// WorkflowDefinition binds a workflow handler to a logical name and default queue.
type WorkflowDefinition struct {
    Name      string
    TaskQueue string
    Handler   WorkflowFunc
}

// WorkflowFunc is the generated workflow entry point. WorkflowContext wraps the underlying
// engine-specific context and offers activity execution and metadata helpers.
type WorkflowFunc func(ctx WorkflowContext, input any) (output any, err error)

type WorkflowContext interface {
    Context() context.Context
    WorkflowID() string
    RunID() string
    ExecuteActivity(ctx context.Context, req ActivityRequest, result any) error
    Logger() Logger
    Metrics() Metrics
}

// ActivityDefinition registers an activity handler with optional defaults (retry, timeout).
type ActivityDefinition struct {
    Name    string
    Handler ActivityFunc
    Options ActivityOptions
}

type ActivityFunc func(ctx context.Context, input any) (output any, err error)

// WorkflowStartRequest describes how to launch a workflow execution.
type WorkflowStartRequest struct {
    ID        string
    Workflow  string
    TaskQueue string
    Input     any
    RetryPolicy RetryPolicy
}

// ActivityRequest contains the info needed to schedule an activity from a workflow.
type ActivityRequest struct {
    Name        string
    Input       any
    Queue       string
    RetryPolicy RetryPolicy
    Timeout     time.Duration
}

// WorkflowHandle allows the runtime (or callers) to interact with a running workflow.
type WorkflowHandle interface {
    Wait(ctx context.Context, result any) error
    Signal(ctx context.Context, name string, payload any) error
    Cancel(ctx context.Context) error
}
```

Generated code reuses shared structs (`WorkflowDefinition`, `ActivityDefinition`, `WorkflowStartRequest`, `ActivityRequest`, etc.) provided by the runtime package. Per-design artefacts (e.g., the workflow handler functions, activity handlers, and start parameters) are generated and then registered through the injected `WorkflowEngine`. Workflow handlers operate purely against `WorkflowContext` and `ActivityRequest`, keeping engine-specific logic isolated in the adapter. The hook system underneath leverages [Pulse](../../pulse) so runtime events are fan-out ready (memory, streaming, logging can all subscribe without tight coupling).

## Planner Contract

Each agent design supplies a planner implementation that satisfies the runtime’s planner interface (e.g., `PlanStart`, `PlanResume`). Generated workflows invoke planners at two key points:

- **PlanStart** – called before any tool execution when a run begins. Receives:
  - An `AgentContext` containing a `MemoryReader`, mutable `AgentState`, invocation metadata (caps, labels, retry hints), logging/stream helpers, access to the runtime hook bus, and a `ModelClient` for invoking LLMs.
  - DSL-defined inputs (messages, run context) and caller-provided labels.
- **PlanResume** – called after a set of tool executions completes. Receives the same context plus collected `ToolResult` events and updated caps/hints (e.g., remaining tool calls, time left, consecutive failure count).

Planners return a `PlanResult` structure containing:

- `ToolCalls` – ordered list of tool invocations (name + payload) to schedule. Empty when the planner wants to synthesize a final answer.
- `FinalResponse` – textual or structured response when the planner decides to terminate the run.
- `Notes` / `Annotations` – optional planner notes persisted via the memory subsystem and available to subscribers (streaming, observability).
- `AgentContext` now exposes `EmitAssistantMessage` / `EmitPlannerNote` helpers so planners can stream partial updates without dealing with the hook bus directly. The `planner` package also ships `ConsumeStream(ctx, streamer, agentCtx)` which drains `model.Streamer`s, forwards text/thinking chunks via those helpers, aggregates the final text/tool-call set, and hands the planner a `StreamSummary` to translate into a `PlanResult`.
- `RetryHint` – optional hint for the runtime/policy engine to adjust allowlists after failures (e.g., invalid arguments).

Runtime guarantees to the planner:

1. **Memory access** – `MemoryReader` provides the full run history up to the current turn (messages, tool-call records, prior annotations). Helper methods allow the planner to append annotations when permitted.
2. **Hook integration** – planners can emit custom events (streaming updates, audit logs) without directly coupling to persistence/telemetry.
3. **Model access** – planners receive a `ModelClient` (registered during runtime bootstrap) to call provider SDKs (Bedrock, OpenAI, etc.) through a consistent interface.
4. **Policy updates** – caps and allowlists are recalculated between planner invocations based on tool outcomes, retry hints, and policy decisions, mirroring the AURA pattern.

This contract keeps planner implementations focused on reasoning/business logic while the runtime handles durability, policy enforcement, and tool orchestration.

```go
// Planner is the interface generated code expects.
type Planner interface {
    PlanStart(ctx context.Context, input PlanInput) (PlanResult, error)
    PlanResume(ctx context.Context, input PlanResumeInput) (PlanResult, error)
}

type PlanInput struct {
    Messages   []AgentMessage
    RunContext RunContext
    Agent      AgentContext
}

type PlanResumeInput struct {
    Messages    []AgentMessage
    RunContext  RunContext
    Agent       AgentContext
    ToolResults []ToolResult
}

type PlanResult struct {
    ToolCalls     []ToolCallRequest
    FinalResponse *FinalResponse
    Notes         []PlannerAnnotation
    RetryHint     *RetryHint
}
```

### Retry Hints & Reasons

Planners can guide the runtime after a failed turn by returning a `RetryHint`. The type includes a `Reason` (enumerated below), the affected `Tool`, optional schema metadata (`MissingFields`, `ExampleInput`, `PriorInput`), user prompts (`ClarifyingQuestion`, human-readable `Message`), and a `RestrictToTool` flag for single-tool retries. The runtime immediately publishes a `retry_hint` hook event, forwards the hint to the policy engine on the next turn, and lets policies tighten caps or disable conflicting tools without modifying planner code.

`RetryReason` values mirror the current runtime implementation:

- `invalid_arguments` – payload failed schema validation.
- `missing_fields` – required arguments were omitted.
- `malformed_response` – downstream service returned data the agent could not parse.
- `timeout` – the tool exceeded its SLA.
- `rate_limited` – platform throttled the request.
- `tool_unavailable` – dependency outage or disabled integration.

This taxonomy gives higher-level orchestrators (and AURA-style policy engines) a consistent surface for retry vs. fail-fast decisions while keeping generated code generic.

## Runtime Package Constructs

The runtime module is split into focused subpackages. Generated code should import these rather than re-declare shared structs.

### `agents/runtime/engine`

- `WorkflowEngine` interface plus the upcoming Temporal adapter. Adapter responsibilities (mirroring the orchestrator service in AURA):
  1. **Worker wiring**: register runtime-generated workflows/activities with `worker.New` and honor per-activity queue choices (e.g., ADA vs. AD vs. Pulse queues in `ada_loop.go`). Workers should auto-start the first time a workflow launches while `WorkerController` exposes explicit `Start/Stop` hooks for advanced orchestration.
  2. **Context injection**: wrap Temporal activity contexts with `engine.WithWorkflowContext` before invoking runtime handlers so nested agent execution can recover the parent `WorkflowContext`.
  3. **Activity options**: translate DSL-derived `ActivityOptions` (queue, timeout, retry policy) into Temporal `workflow.ActivityOptions` when scheduling planner/tool activities (`workflow.ExecuteActivity` / `ExecuteActivityAsync`).
  4. **Workflow start**: map `WorkflowStartRequest` (including the new `Workflow` field) to Temporal `StartWorkflowOptions`, including memo/search attributes for observability (as AURA does with session/turn metadata). Support registering multiple workflows (one per agent) instead of enforcing a single definition.
  5. **Signals & handles**: back `WorkflowHandle` with Temporal `WorkflowRun`, supporting `Wait`, `Signal`, `Cancel`, and `SignalWithStart` patterns needed by session lifecycle workflows, and expose `WorkflowContext.SignalChannel(name)` so workflows can subscribe to pause/resume/human-in-loop signals in a backend-agnostic way.
  6. **Data conversion & tracing**: allow injection of custom data converters (like AURA’s `dataconverter.go`) and propagate OTEL/Clue interceptors so workflow logs/metrics integrate with existing tooling. Use the official Temporal OTEL interceptors (`go.temporal.io/sdk/contrib/opentelemetry`) on both the client (when the adapter creates it) and the workers to get full RPC traces without manual wiring.
  7. **Telemetry propagation**: capture the request context supplied to `runtime.StartRun` (Clue logger + parent span) and rehydrate it inside `WorkflowContext.Context()` **and** the activity handler contexts (via `telemetry.MergeContext`) so activities/agents inherit the same structured logging/tracing metadata even when they don’t call `wfCtx.Context()` explicitly. Combine this with the Temporal OTEL interceptors so traces flow cleanly from the initial RPC through Temporal’s gRPC calls and into workflow/activity spans.
- Core helper types (`WorkflowDefinition`, `ActivityDefinition`, `WorkflowStartRequest`, `ActivityRequest`, `WorkflowHandle`, `WorkflowContext`) remain engine-agnostic so other engines can implement the same adapter contract.

Adapters should expose options that make these behaviors turnkey: accepting either a user-provided `client.Client` or a `client.Options` struct so the adapter can instantiate and own the client (and close it when done), an `InstrumentationOptions` block that enables OTEL tracing/metrics by default using `go.temporal.io/sdk/contrib/opentelemetry`, and a `DisableWorkerAutoStart` switch for tests that want to manage the worker lifecycle manually.

### `agents/runtime/runtime`

- `Runtime` orchestrator that holds the `WorkflowEngine`, `MemoryStore`, policy engine, stream/session adapters, and telemetry hooks. Options allow swapping in alternative engines/stores beyond the Temporal + Mongo defaults.  
- Registration helpers (`RegisterAgent<AgentName>`, `RegisterToolset<...>`) generated per design plug into this orchestrator.  
- Implements the plan/execute/resume loop using the injected engine, classifies errors (retryable vs fatal), and publishes outcome events through the hook bus.
- Exposes `PauseRun` / `ResumeRun` helpers and drains pause/resume signals inside the workflow loop, updating the run store and emitting `run_paused` / `run_resumed` hook events so higher-level orchestrators (or humans) can safely intervene mid-turn.
- Surfaces planner `RetryHint`s by emitting `retry_hint` hook events and copying the hint into the next `policy.Input`, so allowlists/caps can react without touching planner code.
- Planner activities inherit the DSL-derived activity options: codegen registers `PlanStart`/`PlanResume` with default retry + timeout settings and the runtime mirrors those options when scheduling activities so Temporal consistently enforces them even if per-call overrides are added later.
- During registration every agent passes the generated `tool_specs.Specs`, allowing the runtime to look up `tools.JSONCodec` instances by fully qualified tool name. Persistence layers (memory, hooks, streaming) then serialize tool payloads/results to canonical JSON before emitting events, matching the legacy tools plugin behavior.
- `ExecuteToolActivity` is scheduled from the workflow using the agent-specific activity name and per-toolset queues. The activity handler (generated per agent) simply invokes `Runtime.ExecuteToolActivity`, which decodes the JSON payload, dispatches the registered toolset implementation, and re-encodes the result for the workflow.

### `agents/runtime/policy`

- `PolicyEngine` interface (and helpers) invoked once per turn to compute the effective allowlist and caps.  
- Receives tool metadata (names, tags), retry hints, per-turn counters, labels, and invocation state.  
- TODOs to cover in implementation:
  1. **Runtime override path** – allow policy decisions to adjust remaining caps each turn (e.g., decrement tool-call budgets after failures).
  2. **Policy context richness** – make sure callbacks get tool tags, retry hints, caller labels, and other context needed to reproduce AURA-style behavior exactly.

```go
type PolicyEngine interface {
    Decide(ctx context.Context, input PolicyInput) (PolicyDecision, error)
}

type PolicyInput struct {
    RunContext          RunContext
    Tools               []ToolMetadata
    RetryHint           *RetryHint
    RemainingCaps       CapsState
    Requested           []ToolHandle
    Labels              map[string]string
}

type PolicyDecision struct {
    AllowedTools []ToolHandle
    Caps         CapsState
    DisableTools bool
    Labels       map[string]string
}
```

Key runtime inputs to the policy engine:

- `Tools` mirrors every candidate tool available to the planner on that turn (description, tags, IDs). Policies trim this list to the allowlist returned in `AllowedTools`.
- `RetryHint` carries the planner’s structured recommendation after a tool failure (see “Retry Hints & Reasons” below). The runtime forwards it verbatim and also emits a `retry_hint` hook event for observability.
- `Requested` reflects the exact tool calls the planner just asked for. Policies can use it to block/allow individual calls or to implement circuit breakers without scanning the whole metadata set again.

### `agents/runtime/planner`

- Planner interfaces (`Planner`, `PlanInput`, `PlanResumeInput`, `PlanResult`, `AgentContext` helpers). Generated code and feature modules import these so planner implementations remain decoupled from workflow internals.
- Provides helpers for interacting with `MemoryReader`, `ModelClient`, hooks, and policy counters.

### `agents/runtime/interrupt`

- Implements the pause/resume controller that listens on well-known signal names (`goaai.runtime.pause`, `goaai.runtime.resume`), exposes non-blocking `PollPause` / blocking `WaitResume` helpers for the workflow loop, and defines the request payloads used by transports (`PauseRun` / `ResumeRun`). Controllers update run records, emit hook events, and allow resume payloads to inject additional planner messages before continuing execution.

### `agents/runtime/hooks`

- Defines the runtime hook/event system (`RuntimeEvent`, `EventBus`, `Subscriber`) used to publish events such as workflow lifecycle changes, tool activity, planner notes, and synthesized responses.
- Generated code and runtime components emit events through this bus; feature modules (memory, streaming, telemetry) register subscribers built on Pulse.
- Include default subscribers (e.g., structured logging) and allow custom modules to attach their own.

Standard events include:

| Event Type             | Payload Fields                                         | Emitted When                               |
|------------------------|--------------------------------------------------------|--------------------------------------------|
| `workflow_started`     | run ID, agent ID, initial RunContext                   | Workflow entry point                       |
| `workflow_completed`   | run ID, agent ID, final status                         | Workflow exit                              |
| `tool_call_scheduled`  | run ID, tool name, payload, queue                      | Before scheduling ExecuteActivity          |
| `tool_result_received` | run ID, tool name, result payload, duration, error?    | After activity returns                     |
| `planner_note`         | run ID, note text, labels                              | Planner emits annotations                  |
| `assistant_message`    | run ID, response text/structured output                | Planner returns FinalResponse              |
| `retry_hint`           | run ID, tool name, reason, message                     | Planner/tool failure triggers adjustments  |
| `memory_appended`      | run ID, memory event ID (optional)                     | Memory store persists new events           |

The Pulse-backed bus ensures ordered delivery per workflow run and supports fan-out with retry semantics.

### `agents/runtime/memory`

- `MemoryStore`, `MemorySnapshot`, `MemoryEvent`, and helper utilities to persist run history (user messages, tool-call records, planner annotations, emitted summaries, etc.).
- Core interface: `LoadRun(ctx, runID) (MemorySnapshot, error)` and `AppendEvents(ctx, runID, events ...MemoryEvent) error`. A `MemoryReader` view is injected into the planner/agent context so custom logic can inspect or append annotations.
- `MemoryEvent` is a discriminated union aligned with hook events (e.g., `MemoryEventToolCall`, `MemoryEventToolResult`, `MemoryEventPlannerNote`, `MemoryEventAssistantMessage`).
- Subscribes to the runtime hook bus, listening for events like `tool_call`, `tool_result`, `planner_note`, and `assistant_message`, then storing them durably. Other subscribers (streaming, observability) can listen to the same events without coupling to storage.
- Storage is pluggable. We ship an in-memory store for tests and a MongoDB-backed implementation as the default production choice; teams can provide Redis/custom stores by implementing the interface.
- Memory appenders rely on the tool codecs described above so every stored tool payload/result is a `json.RawMessage`. Downstream consumers can deserialize with the same codec guarantees, ensuring schema drift is impossible.
- **Compatibility target (based on `~/src/aura/services/session`)**: ensure the memory subsystem can represent a broad set of event categories (message, thinking, tool_call, tool_result, tool_call_updated, workflow, diagnostics start/end, inference request/response/error, token usage, annotations) without being tied to a single product domain. The payload schema stays generic (`Data any`) but we standardize labels so adapters can surface org/agent/session metadata, actor info, trace IDs, and turn sequencing. The Mongo-backed store must also support:
  - Persisting large payloads via pluggable blob refs (S3 today) to avoid oversized documents.
  - Cursor-based pagination for session transcripts (occurred_at + `_id`), mirroring `ListSessionEventsPaged`.
  - Filtering/search hooks so feature modules can implement “search sessions” / “search tool failures” using the same underlying documents.
  These requirements mean the Mongo store exposes lower-level collection handles (or helper query methods) so higher-level services (session APIs, diagnostics tooling, or any other domain-specific module) can reuse the same data without re-ingesting events. Domain-specific behaviors (diagnostics summaries, agent fact catalogs, etc.) should live in feature modules that compose with these generic primitives rather than being hardcoded into goa-ai.
- `features/memory/mongo/clients/mongo` hosts the actual MongoDB client implementation following the `clients/mongo` convention. The package exposes a `Client` interface plus a concrete implementation that performs the BSON marshalling/index setup, and cmg-generated mocks (`clients/mongo/mocks`) let unit tests exercise store logic without hand-written fakes.

### `agents/runtime/tools`

- Shared JSON codec and registry primitives (`JSONCodec`, `TypeSpec`, `ToolSpec`) referenced by all generated tool packages.
- Keeps marshaling logic consistent with the legacy tools plugin so Temporal data converters, memory persistence, and inference adapters can rely on the same helpers without duplicating serialization code.
- Generated code stores both typed codecs (for callers) and untyped codecs (for registries/policy) so the runtime can persist tool payloads/results verbatim.

### `agents/runtime/model`

- `ModelClient` interface plus provider adapters (e.g., `model/bedrock`, `model/openai`). The interface now exposes both `Complete` (unary) and `Stream`, with the latter returning a `model.Streamer` that yields `model.Chunk` events (`text`, `tool_call`, `thinking`, `usage`, `stop`).
- Request/response types (`model.Request`, `model.Response`, `model.Chunk`) that planners use to invoke or stream LLM responses. Requests gained a `Stream` hint and optional `ThinkingOptions` (enable/disable, budget tokens, disable reason) so planners can control Bedrock-style “thinking” modes explicitly.
- Runtime bootstrap registers the desired provider client; planners access it via `AgentContext.Model()` and decide per-turn whether to call `Complete` or `Stream`. The runtime capture sink and Pulse sink already accept partial assistant replies, so streaming chunks can flow to observers without extra plumbing.
- DSL does not currently expose model configuration—the expectation is that applications wire providers in code, keeping the DSL focused on contract, not infrastructure.
- Telemetry follows OTEL conventions: runtime/agent contexts always expose a tracer/logger/metrics handle (wired to Clue by default). When no provider is configured, no-op implementations are injected so callers never need nil checks.

### Feature Modules – Model Providers & Policy

- `features/model/bedrock` adapts the Bedrock Runtime (Anthropic Messages API) to `model.Client`. Callers pass the AWS SDK runtime client plus defaults (model ID, max tokens, temperature, thinking budget). The adapter now supports both Converse (unary) and ConverseStream, translating Bedrock streaming events (text deltas, tool_use buffers, reasoning/thinking messages, usage reports) into `model.Chunk`s while automatically enabling/disabling thinking headers based on the planner’s `ThinkingOptions` and whether tools are active.
- `features/model/openai` wraps the `github.com/sashabaranov/go-openai` client. It converts `model.Request` into `ChatCompletionRequest` (messages, tools/functions, temperature) and returns assistant text + tool calls + token usage. Helper `NewFromAPIKey` simplifies bootstrap when teams just want to hand an API key + default model. Streaming is not yet implemented for OpenAI, so the adapter reports `model.ErrStreamingUnsupported` and planners should fall back to `Complete` until we add the Chat Completions SSE path.
- `features/policy/basic` implements `policy.Engine` with allow/block lists (by tool ID or tag) and automatic handling of planner retry hints. When a hint requests `RestrictToTool`, the engine collapses the allowlist and tightens the remaining tool-call budget; when a hint reports `tool_unavailable`, the engine removes that tool for the next turn. Decision labels include `policy_engine=basic` (and `policy_hint=<reason>` when applied) so telemetry subscribers can trace policy choices.

### MCP Integration (Service + Runtime)

We preserve Goa’s “declare an MCP server in the service DSL” capability, but channel it through the new agent runtime in a codegen-driven way—no runtime reflection on DSL expressions.

- **Service-side DSL stays the entry point**: Teams keep using `MCPServer("suite", …)` inside their Goa services to describe tools/resources/prompts/subscriptions. The DSL captures this as a suite descriptor during evaluation.
- **Codegen emits both server transports and runtime glue**:
  1. *MCP server (optional)*: If a service wants to expose the suite to external MCP clients, the generator emits the JSON-RPC/SSE server just like today (using Goa’s JSON-RPC generator plus retry helpers). This is entirely codegen-driven.
  2. *Runtime registration helper*: Regardless of whether a server is emitted, goa-ai now generates a small package (e.g., `gen/<svc>/mcp/<suite>/register.go`) that exports a strongly typed helper such as `Register<svc><suite>Toolset(rt *agentsruntime.Runtime, caller mcpfeature.Caller)`. This helper encapsulates all tool metadata (names, codecs, policy metadata) produced during codegen.
- **Agent opt-in via config**: When an agent uses `UseMCPToolset`, its generated config exposes an `MCPCallers` map keyed by toolset ID (e.g., `ChatAgentAssistantAssistantMcpToolsetID`). Application code instantiates the desired transport (`mcpruntime.NewHTTPCaller`, `NewSSECaller`, `NewStdioCaller`, or the Goa-generated JSON-RPC caller) and assigns it in the map. The agent registry then invokes the generated helper (`Register<Service><Suite>Toolset`) automatically, wiring codecs, hook emissions, structured telemetry, and retry hints without extra glue.
- **Agent DSL references MCP suites**: Within the agent design, we add `UseMCPToolset("atlas", "docs")` (or similar) so the agent codegen includes the MCP tools in its toolset/codecs. Under the hood this simply imports the generated helper package and ensures the runtime knows which tool names belong to the suite. No runtime introspection—everything is wired through generated Go code.
- **Policy & telemetry alignment**: Because MCP tools reuse the same codec/spec packages, policy engines get consistent `ToolMetadata` (tags, descriptions) and telemetry hooks record normal `tool_call_scheduled`/`tool_result_received` events. OTEL context propagation lives in the MCP client transport (`features/mcp/runtime`), not in codegen.
- **Deterministic helper naming**: Every helper follows one rule—`Register<ServiceGoName><SuiteGoName>Toolset`. Both segments come from Goa’s `GoName` (e.g., service `assistant` → `Assistant`, suite `docs-mcp` → `DocsMCP`). There is no stutter stripping or special casing, so teams can reference helpers predictably regardless of how service/suite names overlap.

Net result: Goa services retain the ability to generate full MCP servers, while agents gain seamless access to the same MCP toolsets via generated registration helpers—no duplication, no runtime reflection, and the entire integration is opt-in per suite.

**Current status**

- DSL + expressions ported: `UseMCPToolset(service, suite)` lets agents reference MCP suites directly. Agent toolset data now tracks external providers (service + suite) so codegen knows how to populate tools from the service-side MCP declarations.
- Codegen emits the runtime registration helper (Register<Service><Suite>Toolset) alongside the MCP server transport files.
- Shared tool specs/codecs: when an agent references an MCP suite, the tool list is realized during `goa gen`, so the generated runtime package already contains the right payload/result structs and `tools.ToolSpec` entries.
- Runtime interface (`features/mcp/runtime.Caller`) now ships HTTP, SSE, and stdio transports (`NewHTTPCaller`, `NewSSECaller`, `NewStdioCaller`) with OTEL propagation and table-driven tests. A `CallerFunc` adapter remains for bespoke transports.
- Goa’s generated MCP clients now emit a `client.NewCaller` helper so services can wrap the JSON-RPC client directly and hand it to the runtime registration helper without writing adapters.
- Agent tool codec generation now materializes standalone payload/result structs and schemas for external MCP suites so consuming agents compile without importing the originating service package.
- The example JSON-RPC bootstrap wraps the generated MCP adapter in a small `mcpassistant.Service` shim so the advanced runtime notification path (which uses `mcpruntime.Notification`) can coexist with the transport-facing `SendNotificationPayload`. Integration tests now exercise this wiring end to end.

**Remaining work**

1. **Docs/examples** – capture the end-to-end MCP flow (service declaration → generated helper → runtime registration) in `docs/dsl.md`, `docs/runtime.md`, and the migration guide. The chat data loop harness should be referenced as the canonical sample.
2. **Tests** – extend unit/integration tests to cover `UseMCPToolset`, generated helper output, and mocked callers, ensuring error paths and retry hints remain deterministic.

### `agents/runtime/session` & `agents/runtime/stream`

- Interfaces (`SessionSink`, `StreamPublisher`) and default no-op/logging implementations for persistence and streaming. The default stream publisher uses Pulse under the hood so events can fan out to multiple subscribers. Feature modules can supply richer adapters (e.g., Pulse to SSE bridge, Pulsar, Kafka).
- `session.Store` includes the in-memory reference plus the Mongo-backed implementation under `features/session/mongo`, providing durable run metadata (status, labels, timestamps) needed for observability and restarts. Other deployments can plug in Redis/SQL/etc. by implementing the same interface. The actual Mongo client lives under `features/session/mongo/clients/mongo`, following the `clients/mongo` pattern and shipping Clue-generated mocks so services/tests can stub Mongo without bespoke fakes. A companion `features/session/mongo/search` module exposes a generic session search repository (filters for org/agent/principal, created/updated ranges, cursor pagination) and a tool-failure repository (filter by tool name, result code, time window) so observability dashboards can port their queries without bespoke Mongo code. Domain-specific collections such as diagnostics summaries or agent facts remain in downstream packages that compose the shared client.
- `stream.Sink` has a Pulse implementation under `features/stream/pulse` that accepts a caller-provided Redis client, publishes JSON envelopes compatible with current Pulse consumers, and includes clue-generated mocks for deterministic tests. A matching subscriber helper (`features/stream/pulse/subscriber`) reuses the same client to attach SSE/UI services to the stream, keeping publish/consume patterns symmetrical.

### `agents/runtime/telemetry`

- Wrappers around Clue (OpenTelemetry) for workflows, activities, and tool executions. Provides a consistent way to emit spans/metrics across agents.

Generated code imports these runtime packages and only generates the per-design artefacts (workflow handlers, activity handlers, registration functions, start requests), keeping the shared contract centralized.

## AURA Alignment Plan

While goa-ai remains vendor-neutral, the first large-scale adopter is the existing AURA system (`~/src/aura`). Reviewing AURA's orchestrator implementation reveals specific patterns that goa-ai must support for a successful migration:

### Core AURA Patterns

#### 1. Agent-as-Tool (ADA Pattern)

AURA's Atlas Data Agent (ADA) functions as both an agent and a tool. When invoked:
- The orchestrator calls `adaLoop` (equivalent to `runLoop` in goa-ai)
- ADA **dynamically discovers** child tool calls across multiple planning iterations
- Each discovered child reports its `ParentToolUseID` pointing back to the ADA method invocation
- The orchestrator tracks discovered children in a `map[string]struct{}` keyed by `ToolUseID`

**Key insight**: Tool discovery happens **progressively** across multiple planner iterations. The first `PlanStart` might return 2 tools, then after those execute, `PlanResume` discovers 3 more. The parent's "expected children total" grows as discovery happens.

#### 2. Dynamic Child Tracking & Progress Updates

AURA implements `updateParentExpectation` (lines 249-299 in `ada_loop.go`):

```go
func (s *adaLoopState) updateParentExpectation(...)
```

**How it works**:
1. After each planner iteration, check if new children were discovered via `registerDiscovered()`
2. If `len(discovered) > lastExpected`, emit update events:
   - `SessionToolCallUpdateEvent` with new `ExpectedChildrenTotal` (session timeline)
   - Pulse `ToolCallEvent` update (real-time streaming to UI)
3. Store `lastExpected = len(discovered)` to avoid duplicate updates

**Critical**: AURA does **NOT** emit "X of Y complete" progress events. Instead:
- Update parent's `ExpectedChildrenTotal` as children are discovered
- UI calculates progress by counting completed tool results vs. current ExpectedChildrenTotal
- This handles dynamic discovery elegantly (total can increase mid-execution)

#### 3. Parallel Tool Execution

AURA's `executeToolCalls` (lines 337-382):
```go
futures := make([]workflow.Future, 0, len(s.toolCalls))
for _, call := range s.toolCalls {
    futures = append(futures, workflow.ExecuteActivity(adCtx, ...))
}
// Later: collect results in order
for i, f := range futures {
    f.Get(ctx, &r)
}
```

**Pattern**: Launch all activities async via Temporal Futures, then block collecting results in-order. Goa-ai's `ExecuteActivityAsync` + `Future.Get()` implements the same pattern.

#### 4. Turn Sequencing

AURA maintains a `turnContext` struct (lines 18-25):
```go
type turnContext struct {
    TurnID   string
    StreamID string
    Seq      sequencer  // Monotonic counter
}
```

Every event published gets:
- `TurnID` for grouping
- `SeqInTurn` via `s.turn.Seq.Next()` for deterministic ordering

**Implementation**: `turnSequencer` (in `turn_seq.go`) is a simple counter with `Next()` and `Current()` methods. Goa-ai's `turnSequencer` mirrors this exactly.

#### 5. Event Types & Publishing

AURA publishes to **two** destinations for most events:

**Session Timeline** (durable):
```go
gentypes.SessionEvent{
    SessionID: s.sessionID,
    ChatTurnContext: &gentypes.ChatTurnContext{
        TurnID:    s.turn.TurnID,
        SeqInTurn: seq,
    },
    Details: &gentypes.SessionToolCallUpdateEvent{...}
}
```

**Pulse Stream** (real-time):
```go
gentypes.ToolCallEvent{
    ToolName:              call.Name,
    ToolUseID:             call.ToolUseID,
    ParentToolUseID:       s.parentToolUseID,
    ExpectedChildrenTotal: updated,
}
```

**Goa-ai mapping**:
- Session timeline → `memory.Store.AppendEvents`
- Pulse stream → `stream.Sink` via `hooks.StreamSubscriber`
- Both consume from the same `hooks.Bus` events

#### 6. Parent-Child Field Usage

Every tool call/result event includes:
- `ParentToolUseID` (empty for top-level planner calls)
- `ExpectedChildrenTotal` (on parent only, updated as children discovered)

**UI rendering**: Frontend uses these fields to:
- Build a tree view of tool calls
- Show "3 of 5 children complete" by comparing:
  - Completed results with `ParentToolUseID == X`
  - Parent's current `ExpectedChildrenTotal`

### Migration Strategy

With these patterns understood, AURA migration becomes:

**Phase 1: Infrastructure** ✅ (Complete)
- Parallel tool execution via `ExecuteActivityAsync` + `Future`
- TurnID support in `run.Context`
- Parent-child tracking fields (`ParentToolCallID`, `ExpectedChildrenTotal`)
- Turn sequencing (`turnSequencer`, `SeqInTurn`)

**Phase 2: Dynamic Child Tracking** ✅ (Infrastructure Complete)
- ✅ Added `ToolCallUpdatedEvent` to hooks with full integration
- ✅ Implemented `childTracker` structure with discovery methods
- ✅ Added event stamping for turn sequencing
- ⏳ **Deferred**: Full nested agent-as-tool loops with dynamic discovery
  - Requires tool activities to run their own internal planning loops
  - See AURA's `adaLoop` pattern: tools that execute sub-tools and track children
  - This will be implemented as part of agent-as-tool codegen (Phase 3)
  - Current infrastructure (events, tracker, fields) supports future implementation

**Phase 3: Agent-as-Tool Implementation** 

This phase implements the full agent-as-tool pattern where an agent can be invoked as a tool
by other agents, running its own internal planning loops and dynamically discovering child tools.

### Architecture Summary

**Key Design Decisions**:

1. **Uniform Workflow Execution**: The workflow ALWAYS calls `ExecuteActivityAsync` for ALL tools.
   There are ZERO conditionals or runtime type checks in the workflow code.

2. **Uniform Activity Implementation**: Each agent's generated activity ALWAYS calls `toolset.Execute`.
   The activity doesn't know if it's executing a service-based tool, custom tool, or agent-tool.

3. **Dispatch via Toolset Registration**: The dispatch logic lives in the toolset registration's
   `Execute` function. This function is:
   - Auto-generated by codegen for service-based tools (calls service client)
   - Auto-generated by codegen for agent-tools (calls `ExecuteAgentInline`)
   - User-provided for custom/server-side tools (calls user implementation)

4. **Agent-as-Tool = Generated Execute Function**: For agents with `Exports` blocks, codegen
   generates a `NewAgentToolsetRegistration` helper that returns a registration with a pre-wired
   Execute function. This function calls `rt.ExecuteAgentInline` to run the nested agent inline.

**Data Flow**:
```
Workflow (runtime/workflow.go)
  └─> ExecuteActivityAsync (uniform, always)
      └─> Agent's ExecuteToolActivity (generated)
          └─> toolset.Execute (from registration)
              ├─> Service Client (for service-based tools)
              ├─> User Implementation (for custom tools)
              └─> rt.ExecuteAgentInline (for agent-tools)
                  └─> runLoop (reuses existing planning loop!)
```

**Benefits**:
- ✅ Zero runtime type detection or conditionals
- ✅ All dispatch logic is codegen-driven (build-time decisions)
- ✅ Workflow code is trivially simple and uniform
- ✅ Activity code is trivially simple and uniform
- ✅ Agent-as-tool is just another Execute function (no special cases)
- ✅ Easy to add new tool types (just generate different Execute functions)
- ✅ Nested agents reuse existing runLoop (perfect composition)
- ✅ Deterministic workflow replay (no branching on runtime state)

### Design: Direct Loop Invocation (Elegant & Composable)

**Key Insight**: Agent-as-tool **reuses the existing `runLoop`** instead of starting separate workflows.
This is simpler, more composable, and provides better determinism for workflow replay.

**Architecture**:
```
Parent Agent
  └─ executeToolCalls()
      ├─ Regular Tool → ExecuteActivityAsync()
      └─ Agent Tool → Call runLoop() directly (same workflow context!)
          └─ Nested agent's planning loop executes inline
          └─ Child tracker updated during execution
```

### Prerequisites

✅ Infrastructure already implemented (Phase 1 & 2):
- `ToolCallUpdatedEvent` with `ExpectedChildrenTotal` field
- `childTracker` structure and methods in `workflow.go`
- `ParentToolCallID` field on `ToolCallScheduledEvent`
- Parallel tool execution via `ExecuteActivityAsync`
- Turn sequencing support

✅ `runLoop` is already well-factored for reuse (all dependencies passed as parameters)

### Runtime Types

Before implementing the steps, we need to define the core runtime types that enable the toolset registration pattern.

**Add to `agents/runtime/runtime/types.go`**:

```go
// ToolsetRegistration holds the metadata and execution logic for a toolset.
// Users register toolsets by providing an Execute function that handles all
// tools in the toolset. Codegen auto-generates registrations for service-based
// tools and agent-tools; users provide registrations for custom/server-side tools.
type ToolsetRegistration struct {
    // Name is the qualified toolset name (e.g., "service.toolset_name")
    Name string

    // Description provides human-readable context for tooling.
    Description string

    // Metadata captures structured policy metadata about the toolset.
    Metadata policy.ToolMetadata

    // Execute invokes the concrete tool implementation for a given tool call.
    // Returns a ToolResult containing the payload, telemetry, errors, and retry hints.
    //
    // For service-based tools, codegen generates this function to call service clients.
    // For agent-tools (Exports), codegen generates this to call ExecuteAgentInline
    // and convert RunOutput to ToolResult.
    // For custom/server-side tools, users provide their own implementation.
    Execute func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error)

    // Specs enumerates the codecs associated with each tool in the set.
    // Used by the runtime for JSON marshaling/unmarshaling and schema validation.
    Specs []tools.ToolSpec

    // TaskQueue optionally overrides the queue used when scheduling this toolset's activities.
    TaskQueue string
}
```

**Add to `agents/runtime/runtime/runtime.go`**:

```go
// Runtime additions for toolset registry
type Runtime struct {
    // ... existing fields ...
    
    toolsets map[string]ToolsetRegistration  // Keyed by toolset name
    mu       sync.RWMutex                     // Protects toolsets map
}

// RegisterToolset registers a toolset with the runtime. The toolset's Execute
// function will be called when any tool in the toolset is invoked.
//
// For service-based tools, use codegen helpers like NewAtlasDataToolsetRegistration.
// For agent-tools, use codegen helpers like NewAtlasDataAgentToolsetRegistration.
// For custom tools, provide your own ToolsetRegistration with a custom Execute function.
func (r *Runtime) RegisterToolset(reg ToolsetRegistration) error {
    if reg.Name == "" {
        return errors.New("toolset name is required")
    }
    if reg.Execute == nil {
        return errors.New("toolset execute function is required")
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    r.addToolsetLocked(reg)
    return nil
}

// LookupToolset retrieves a registered toolset by name. Returns false if the
// toolset is not registered.
func (r *Runtime) LookupToolset(id string) (ToolsetRegistration, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    ts, ok := r.toolsets[id]
    return ts, ok
}
```

**Update `ToolInput` to include `ToolsetName`**:

```go
// ToolInput is the input for tool execution activities.
type ToolInput struct {
    RunID       string          // Run ID for this execution
    AgentID     string          // Agent executing the tool
    ToolsetName string          // Toolset containing the tool (NEW)
    ToolName    string          // Fully qualified tool name
    ToolCallID  string          // Unique ID for this tool call
    Payload     json.RawMessage // Tool-specific arguments (JSON)
    
    // Context fields for nested execution
    SessionID        string // Session ID for this execution
    TurnID           string // Turn ID for event sequencing
    ParentToolCallID string // Parent tool call (for agent-as-tool)
}
```

### Implementation Steps

#### Step 1: Add ExecuteAgentInline Public API

**Goal**: Provide a high-level entry point for agent-as-tool execution that starts from tool arguments (messages) rather than requiring a pre-existing plan.

**Add to `runtime/runtime.go`**:
```go
// ExecuteAgentInline runs an agent's complete planning loop inline within the
// current workflow context. This is the entry point for agent-as-tool execution,
// where one agent invokes another agent as a tool call.
//
// Unlike ExecuteWorkflow (which starts a new durable workflow), ExecuteAgentInline
// runs the nested agent synchronously in the same workflow execution. This provides:
//   - Deterministic workflow replay (nested execution is part of parent workflow history)
//   - Zero overhead (no separate workflow or marshaling)
//   - Natural composition (nested agent completes before parent continues)
//
// The nested agent runs its full plan/execute/resume loop:
//  1. Calls PlanStart with the provided messages
//  2. Executes any tool calls (which may themselves be agent-tools)
//  3. Calls PlanResume after tool results
//  4. Repeats until the agent returns a final response
//
// Parent-child tracking: If nestedRunCtx.TurnID is set, all events from the nested
// agent will be tagged with that TurnID and sequenced relative to the parent's events.
// The nested agent inherits the parent's turn sequencer for consistent event ordering.
//
// Policy and caps: The nested agent uses its own RunPolicy (defined in its Goa design).
// It does NOT inherit the parent's remaining tool budget - each agent enforces its own caps.
//
// Memory: The nested agent has its own memory scope (separate runID). Tool calls and
// results are persisted under the nested runID, allowing the nested agent to be
// replayed or debugged independently.
//
// Parameters:
//   - wfCtx: The parent workflow context. The nested agent shares this context for
//     deterministic execution and can schedule its own activities.
//   - agentID: The fully qualified agent identifier (e.g., "service.agent_name").
//   - messages: The conversation messages to pass to the nested agent's planner.
//   - nestedRunCtx: Run context for the nested execution, including the nested runID
//     and optional parent tool call ID for tracking.
//
// Returns the nested agent's final output or an error if planning or execution fails.
// Tool-level errors (e.g., a tool call failed) are captured in the agent's output,
// not returned as errors - only infrastructure failures return errors.
func (r *Runtime) ExecuteAgentInline(
    wfCtx engine.WorkflowContext,
    agentID string,
    messages []planner.AgentMessage,
    nestedRunCtx run.Context,
) (RunOutput, error) {
    // 1. Look up agent registration
    // 2. Create agent context with nested memory scope
    // 3. Call PlanStart to get initial plan
    // 4. Call runLoop (private) with initial plan
    // 5. Return output
}
```

#### Step 2: Update Tool Specs to Include IsAgentTool Metadata

**Goal**: Let codegen tell the runtime which tools are agent-tools via tool spec metadata.

**DSL Example** (already supported):
```go
Agent("atlas_data_agent", func() {
    Exports(func() {
        Toolset("atlas.read", func() {
            Tool("GetKeyEvents", func() {
                Args(func() { /* ... */ })
                Return(func() { /* ... */ })
            })
        })
    })
})
```

**Code changes needed**:

1. **Add `IsAgentTool` field to `tools.ToolSpec`** (`agents/runtime/tools/tools.go`):
```go
type ToolSpec struct {
    Name        string
    Description string
    Tags        []string
    // IsAgentTool indicates this tool is an agent-as-tool (inline execution).
    // When true, the tool is executed by calling ExecuteAgentInline instead of
    // scheduling a workflow activity. Set by codegen when processing Exports blocks.
    IsAgentTool bool
    // AgentID is the fully qualified agent identifier (e.g., "service.agent_name").
    // Only set when IsAgentTool is true. Used to look up the agent registration
    // for inline execution.
    AgentID     string
    PayloadCodec JSONCodec
    ResultCodec  JSONCodec
    // ... existing fields ...
}
```

2. **Update codegen to set `IsAgentTool` flag** (`agents/codegen/templates/tool_spec.go.tmpl`):
```go
// Generated tool specs
var specs = []tools.ToolSpec{
    {{- range .Tools }}
    {
        Name:        "{{ .QualifiedName }}",
        Service:     "{{ .Service }}",
        Toolset:     "{{ .Toolset }}",
        Description: "{{ .Description }}",
        Tags: []string{
        {{- range .Tags }}
            "{{ . }}",
        {{- end }}
        },
        {{- if .IsExportedByAgent }}
        IsAgentTool: true,
        AgentID:     "{{ .ExportingAgentID }}",
        {{- end }}
        Payload: tools.TypeSpec{
            Name:   {{ if .Payload }}"{{ .Payload.TypeName }}"{{ else }}""{{ end }},
            {{- if and .Payload .Payload.SchemaVar }}
            Schema: {{ .Payload.SchemaVar }},
            {{- else }}
            Schema: nil,
            {{- end }}
            {{- if .Payload }}
            Codec:  {{ .Payload.GenericCodec }},
            {{- else }}
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
        Result: tools.TypeSpec{
            Name:   {{ if .Result }}"{{ .Result.TypeName }}"{{ else }}""{{ end }},
            {{- if and .Result .Result.SchemaVar }}
            Schema: {{ .Result.SchemaVar }},
            {{- else }}
            Schema: nil,
            {{- end }}
            {{- if .Result }}
            Codec:  {{ .Result.GenericCodec }},
            {{- else }}
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
    },
    {{- end }}
}
```

**Key patterns**:
- Codegen sets `IsAgentTool=true` for tools in `Exports` blocks
- Runtime checks this flag (not string matching or type assertions)
- Single source of truth: tool specs registry
- No separate executor registry needed

#### Step 3: Understanding How Users Provide Tool Implementations

**Critical Context**: Before implementing agent-as-tool dispatch, understand how regular tools work:

**Current Tool Execution Pattern**:
```go
// Generated per toolset: ToolsetRegistration struct
type AtlasReadToolsetRegistration struct {
    // Execute is a function that the USER PROVIDES during registration
    // It receives tool name + JSON payload, returns JSON result
    Execute func(ctx context.Context, name string, payload json.RawMessage) (json.RawMessage, error)
}

// User registers their toolset implementation:
registration := AtlasReadToolsetRegistration{
    Execute: func(ctx context.Context, name string, payload json.RawMessage) (json.RawMessage, error) {
        switch name {
        case "list_devices":
            // User's custom implementation or service client call
            return callServiceClient(ctx, payload)
        }
    },
}
rt.RegisterToolset("atlas.read", registration)
```

**For Service-Based Tools**, codegen AUTO-GENERATES the Execute function with optional per-tool adapters and default adapters:
```go
// Generated config struct with optional per-tool adapters
type AtlasDataToolsetConfig struct {
    Client *atlasdata.Client
    
    // Optional: per-tool adapters for custom logic
    // If nil, uses generated default adapter (see below)
    GetKeyEventsAdapter func(ctx context.Context, llmArgs GetKeyEventsArgs) (atlasdata.GetKeyEventsRequest, error)
    AnalyzeDataAdapter  func(ctx context.Context, llmArgs AnalyzeDataArgs) (atlasdata.AnalyzeDataRequest, error)
}

// Generated default adapter (maps matching fields by name/type)
func defaultGetKeyEventsAdapter(ctx context.Context, llmArgs GetKeyEventsArgs) (atlasdata.GetKeyEventsRequest, error) {
    return atlasdata.GetKeyEventsRequest{
        // Auto-generated: only maps fields that match by name and type
        StartTime:  llmArgs.StartTime,
        EndTime:    llmArgs.EndTime,
        EventTypes: llmArgs.EventTypes,
        // Note: OrgID field in service request is NOT set (no matching field in llmArgs)
    }, nil
}

// Generated helper that creates a registration with Execute pre-wired
func NewAtlasDataToolsetRegistration(config AtlasDataToolsetConfig) ToolsetRegistration {
    return ToolsetRegistration{
        Name: "atlas.data",
        Execute: func(ctx context.Context, name string, payload json.RawMessage) (json.RawMessage, error) {
            switch name {
            case "GetKeyEvents":
                // Unmarshal LLM-provided payload into tool args
                var llmArgs GetKeyEventsArgs
                if err := json.Unmarshal(payload, &llmArgs); err != nil {
                    return nil, fmt.Errorf("unmarshal payload: %w", err)
                }
                
                // Use custom adapter if provided, otherwise use default
                adapter := config.GetKeyEventsAdapter
                if adapter == nil {
                    adapter = defaultGetKeyEventsAdapter
                }
                
                // Apply adapter
                req, err := adapter(ctx, llmArgs)
                if err != nil {
                    return nil, fmt.Errorf("adapter: %w", err)
                }
                
                // Call service method
                result, err := config.Client.GetKeyEvents(ctx, req)
                if err != nil {
                    return nil, fmt.Errorf("call service: %w", err)
                }
                
                return json.Marshal(result)
            
            case "AnalyzeData":
                // Similar pattern...
            }
        },
    }
}
```

**Example usages**:

```go
// Example 1: Use required adapters (strong contracts)
registration := NewAtlasDataToolsetRegistration(AtlasDataToolsetConfig{
    Client: atlasDataClient,
    GetKeyEventsAdapter: func(ctx context.Context, llmArgs GetKeyEventsArgs) (atlasdata.GetKeyEventsRequest, error) {
        return atlasdata.GetKeyEventsRequest{ /* map */ }, nil
    },
    GetKeyEventsResultAdapter: func(ctx context.Context, res *atlasdata.GetKeyEventsResult) (*GetKeyEventsResult, error) {
        return &GetKeyEventsResult{ /* map */ }, nil
    },
})

// Example 2: Custom adapters for server-side fields
registration := NewAtlasDataToolsetRegistration(AtlasDataToolsetConfig{
    Client: atlasDataClient,
    GetKeyEventsAdapter: func(ctx context.Context, llmArgs GetKeyEventsArgs) (atlasdata.GetKeyEventsRequest, error) {
        // Inject org_id from context (server-side field)
        orgID := auth.OrgIDFromContext(ctx)
        
        return atlasdata.GetKeyEventsRequest{
            OrgID:      orgID,  // Server-side field injected
            StartTime:  llmArgs.StartTime,
            EndTime:    llmArgs.EndTime,
            EventTypes: llmArgs.EventTypes,
        }, nil
    },
    GetKeyEventsResultAdapter: func(ctx context.Context, res *atlasdata.GetKeyEventsResult) (*GetKeyEventsResult, error) {
        // Transform service result to tool result
        return &GetKeyEventsResult{ /* map */ }, nil
    },
})

// Example 3: Adapters with validation/transformation
registration := NewAtlasDataToolsetRegistration(AtlasDataToolsetConfig{
    Client: atlasDataClient,
    GetKeyEventsAdapter: func(ctx context.Context, llmArgs GetKeyEventsArgs) (atlasdata.GetKeyEventsRequest, error) {
        // Custom logic: validation, defaults, transformation
        if llmArgs.StartTime == "" {
            return atlasdata.GetKeyEventsRequest{}, errors.New("start_time required")
        }
        
        // Populate from multiple sources
        orgID := auth.OrgIDFromContext(ctx)
        timezone := preferences.GetUserTimezone(ctx)
        
        return atlasdata.GetKeyEventsRequest{
            OrgID:      orgID,
            StartTime:  convertToTimezone(llmArgs.StartTime, timezone),
            EndTime:    convertToTimezone(llmArgs.EndTime, timezone),
            EventTypes: llmArgs.EventTypes,
        }, nil
    },
    GetKeyEventsResultAdapter: func(ctx context.Context, res *atlasdata.GetKeyEventsResult) (*GetKeyEventsResult, error) {
        // Example: redact fields, compute aggregates, etc.
        return &GetKeyEventsResult{ /* map */ }, nil
    },
})
```

**Key benefits of this pattern**:
- ✅ **Always optional**: Every tool has an adapter field (even if args match perfectly)
- ✅ **Sensible defaults**: Generated default adapters map matching fields automatically
- ✅ **Full control**: Users can provide custom logic for any reason (not just server-side fields)
- ✅ **No errors on mismatch**: Default adapter leaves unmatched fields at zero value
- ✅ **Type-safe**: Adapters use generated types (compiler catches mistakes)
- ✅ **Clear intent**: Adapter function name makes purpose obvious
- ✅ **Flexible**: Users can inject fields, validate, transform, populate from anywhere
- ✅ **Testable**: Can test adapter functions independently
- ✅ **Simple cases stay simple**: Just pass client, get auto-mapping

**For Server-Side/Custom Tools**, users provide their own Execute function (no service client involved):
```go
// User-provided registration for custom tool
registration := ToolsetRegistration{
    Name: "custom.tools",
    Execute: func(ctx context.Context, name string, payload json.RawMessage) (json.RawMessage, error) {
        switch name {
        case "custom_tool":
            // User's completely custom implementation
            var args CustomArgs
            json.Unmarshal(payload, &args)
            
            // User's custom logic here
            result := doCustomStuff(ctx, args)
            
            return json.Marshal(result)
        }
    },
}
rt.RegisterToolset(registration)
```

### Tool Type Pattern Summary

| Tool Type | Generated Code | User Provides | Dispatch Logic |
|-----------|----------------|---------------|----------------|
| **Service-based** | `NewServiceToolsetRegistration(config)` | Client + required per-tool adapters | Codegen: assert payload → adapters → client call |
| **Agent-tools** | `NewAgentToolsetRegistration(rt)`/`WithTemplates` | Templates (required) | Codegen: render template → `ExecuteAgentInline` |
| **Custom/Server-side** | Tool spec + codec only | Full `ToolsetRegistration.Execute` | User: completely custom logic |
| **MCP-based** (future) | `NewMCPToolsetRegistration(caller)` | MCP client/transport | Codegen: marshal → MCP call → unmarshal |

**Design Philosophy**:
- **Codegen automates the common cases** (service clients, agent-tools)
- **Users control the customization points** (adapters for server-side fields, full Execute for custom logic)
- **Uniform runtime interface** (`ToolsetRegistration.Execute`) regardless of tool type
- **Zero runtime dispatch** in workflow/activity code (just calls `toolset.Execute`)
- **Strong contracts; no fallback coercion**: payload must be the exact generated pointer type; adapters are required for method-backed tools; do not JSON-roundtrip to coerce types at runtime.

**Implementation Note for Service-Based Tools**:

When codegen generates default adapters, it should:

1. **Always generate adapter fields in config** (one per tool, always optional)
2. **Generate default adapter functions** that map matching fields by name and type
3. **Leave unmatched fields at zero value** (no errors - user might populate from elsewhere)
4. **Users can override for any reason** (not just server-side fields - validation, transformation, etc.)

### How Codegen Determines Tool Backing Strategy

Codegen identifies the tool type based on **DSL context and explicit declarations**:

#### 1. Service-Method-Backed Tools

Use `BindTo()` inside the tool definition to indicate it maps to a service method. One-arg binds to the current service; two-arg binds to another service. Example:

```go
Service("atlas_data", func() {
    // Standard Goa service methods
    Method("GetKeyEvents", func() {
        Payload(GetKeyEventsRequest)
        Result(GetKeyEventsResult)
    })
    
    // Agent toolset referencing service methods
    Agent("data_agent", func() {
        Uses(func() {
            Toolset("atlas.data", func() {
                // Pattern 1: Tool args = service request (no adapter needed)
                Tool("list_devices", func() {
                    Args(func() {
                        Field(1, "start_time", String, "Start time")
                        Field(2, "end_time", String, "End time")
                    })
                    BindTo("ListDevices")  // ← Links to method on current service
                })
                
                // Pattern 2: Service has server-side fields
                Tool("get_device", func() {
                    Args(func() {
                        Field(1, "device_id", String, "Device ID")
                        // org_id not exposed to LLM (server-side field)
                    })
                    BindTo("atlas_data", "GetDevice")  // ← Cross-service (service, method)
                    // Codegen generates optional adapter field
                    // Default adapter maps device_id, leaves org_id at zero value
                })
            })
        })
    })
})
```

**Codegen behavior**:
- Uses `goaexpr.MethodExpr.Service` to resolve the target service and imports accordingly (no extra service-name field required).
- Generates per-toolset config with a narrow `Client` interface that matches Goa client method signatures.
- Requires per-method adapters (input + result); no defaults; strong contracts, no JSON fallback coercion.
- Auto-registers each method-backed toolset in `Register<Agent>` using the `<Toolset>Config` from agent config.

#### 2. Agent-Exported Tools

Context: Tool appears in `Exports()` block:

```go
Agent("atlas_data_agent", func() {
    Exports(func() {
        Toolset("atlas.read", func() {
            Tool("GetKeyEvents", func() {
                Args(func() { /* ... */ })
                Return(func() { /* ... */ })
            })
            // NO Method() call - this is an agent-tool, not a service method
        })
    })
})
```

**Codegen behavior**:
- Detects tool is in `Exports` context
- Generates `NewAgentToolsetRegistration` that calls `ExecuteAgentInline`
- Sets `IsAgentTool: true` in tool spec

#### 3. Custom/Server-Side Tools

Neither `Method()` nor `Exports()` context:

```go
Agent("orchestrator", func() {
    Uses(func() {
        Toolset("custom.tools", func() {
            Tool("complex_calculation", func() {
                Args(func() { /* ... */ })
                Return(func() { /* ... */ })
            })
            // NO Method() - not backed by service
            // NOT in Exports() - not an agent-tool
        })
    })
})
```

**Codegen behavior**:
- Only generates tool specs and codecs
- Does NOT generate `NewToolsetRegistration` helper
- User must provide their own `ToolsetRegistration` with Execute function

#### 4. MCP Tools (Future)

Context: Tool uses `UseMCPToolset()`:

```go
Agent("assistant", func() {
    UseMCPToolset("docs_service", "docs_suite")  // ← References MCP suite
})
```

**Codegen behavior**:
- Generates `NewMCPToolsetRegistration` that bridges to MCP caller
- Imports tool specs from the MCP suite definition

### Codegen Decision Tree

```
Is tool in Exports() block?
├─ YES → Generate NewAgentToolsetRegistration (agent-tool)
└─ NO  → Has Method() call?
         ├─ YES → Generate NewServiceToolsetRegistration (service-backed)
         └─ NO  → Is from UseMCPToolset()?
                  ├─ YES → Generate NewMCPToolsetRegistration (MCP-backed)
                  └─ NO  → Generate specs/codecs only (custom tool)
```

This makes the tool backing strategy **explicit in the DSL** and **deterministic at codegen time**.

This pattern ensures:
- ✅ Simple cases work out-of-the-box (no adapter needed)
- ✅ Complex cases require explicit adapters (compiler enforces correctness)
- ✅ Clear error messages when fields mismatch
- ✅ Future DSL can formalize server-side fields

**For Agent-Tools**, codegen generates **minimal glue** that delegates to runtime helpers:

```go
// Generated constants for discoverability
const (
    Name     = "atlas.read"
    Service  = "atlas"
    AgentID  = "atlas.atlas_data_agent"
    
    // Tool names as constants for type-safe configuration
    ToolQueryData    = "atlas.read.query_data"
    ToolAnalyzeData  = "atlas.read.analyze_data"
)

// Default returns the standard agent-tool registration.
// Customize templates or execution logic before calling rt.RegisterToolset().
// Example: Register with options API
// reg, err := atlasread.NewRegistration(rt, "You are a data expert.",
//     atlasread.WithText(atlasread.ToolQueryData, "Query: {{ . }}"),
//     atlasread.WithTemplate(atlasread.ToolAnalyzeData, compiledTmpl),
// )
// if err != nil { /* handle */ }
// rt.RegisterToolset(reg)

// Functional options for mixing text and templates per tool.
type RegistrationOption func(*regCfg)

type regCfg struct {
    texts map[tools.ID]string
    tpls  map[tools.ID]*template.Template
}

func WithText(id tools.ID, s string) RegistrationOption {
    return func(c *regCfg) { if c.texts == nil { c.texts = map[tools.ID]string{} }; c.texts[id] = s }
}
func WithTemplate(id tools.ID, t *template.Template) RegistrationOption {
    return func(c *regCfg) { if c.tpls == nil { c.tpls = map[tools.ID]*template.Template{} }; c.tpls[id] = t }
}
func WithTextAll(ids []tools.ID, s string) RegistrationOption {
    return func(c *regCfg) { if c.texts == nil { c.texts = map[tools.ID]string{} }; for _, id := range ids { c.texts[id] = s } }
}
func WithTemplateAll(ids []tools.ID, t *template.Template) RegistrationOption {
    return func(c *regCfg) { if c.tpls == nil { c.tpls = map[tools.ID]*template.Template{} }; for _, id := range ids { c.tpls[id] = t } }
}

// ToolIDs lists all tools for validation.
var ToolIDs = []tools.ID{ /* generated per toolset */ }

// NewRegistration validates coverage and duplicates, then returns the registration.
// Callers may mix text and templates; a tool cannot be set in both.
func NewRegistration(rt *runtime.Runtime, systemPrompt string, opts ...RegistrationOption) (runtime.ToolsetRegistration, error) {
    var cfg regCfg
    for _, o := range opts { o(&cfg) }
    if err := validateCoverageAndNoDuplicates(cfg.texts, cfg.tpls); err != nil { return runtime.ToolsetRegistration{}, err }
    if len(cfg.tpls) > 0 {
        if err := runtime.ValidateAgentToolTemplates(cfg.tpls, ToolIDs, nil); err != nil { return runtime.ToolsetRegistration{}, err }
    }
    return runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
        AgentID:      AgentID,
        Name:         Name,
        TaskQueue:    /* generated queue */ "",
        SystemPrompt: systemPrompt,
        Templates:    cfg.tpls,
        Texts:        cfg.texts,
    }), nil
}
```

**Runtime implementation** (in `runtime/helpers.go` or `agent_tools.go`):

```go
// AgentToolConfig configures how an agent-tool executes.
type AgentToolConfig struct {
    AgentID string
    // SystemPrompts maps tool name → system prompt.
    // Allows different framing per tool within the same agent.
    SystemPrompts map[string]string
}

// NewAgentToolsetRegistration creates a toolset registration for an agent-as-tool.
// The Execute function calls ExecuteAgentInline with optional per-tool system prompts.
func NewAgentToolsetRegistration(rt *Runtime, cfg AgentToolConfig) ToolsetRegistration {
    agent, ok := rt.Agent(cfg.AgentID)
    if !ok {
        return ToolsetRegistration{} // Will fail on register with clear error
    }

    return ToolsetRegistration{
        Name:        agent.ID,
        Description: fmt.Sprintf("Agent-tool for %s", cfg.AgentID),
        Specs:       agent.Specs,
        Execute:     defaultAgentToolExecute(rt, cfg),
    }
}

func defaultAgentToolExecute(rt *Runtime, cfg AgentToolConfig) func(context.Context, planner.ToolCallRequest) (planner.ToolResult, error) {
    return func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
        wfCtx := engine.WorkflowContextFromContext(ctx)
        if wfCtx == nil {
            return planner.ToolResult{}, fmt.Errorf("workflow context not found")
        }

        // Build messages: agent-wide system prompt (optional), then tool-specific user message from template
        var messages []planner.AgentMessage
        if strings.TrimSpace(cfg.SystemPrompt) != "" {
            messages = append(messages, planner.AgentMessage{Role: "system", Content: cfg.SystemPrompt})
        }
        tmpl := cfg.Templates[tools.ID(call.Name)]
        if tmpl == nil {
            return planner.ToolResult{}, fmt.Errorf("template not configured for tool: %s", call.Name)
        }
        var b strings.Builder
        if err := tmpl.Execute(&b, call.Payload); err != nil {
            return planner.ToolResult{}, fmt.Errorf("render tool template for %s: %w", call.Name, err)
        }
        messages = append(messages, planner.AgentMessage{Role: "user", Content: b.String()})

        // Build nested run context from explicit ToolCallRequest fields
        nestedRunCtx := run.Context{
            RunID:     NestedRunID(call.RunID, call.Name),
            SessionID: call.SessionID,
            TurnID:    call.TurnID,
        }

        output, err := rt.ExecuteAgentInline(wfCtx, cfg.AgentID, messages, nestedRunCtx)
        if err != nil {
            return planner.ToolResult{}, fmt.Errorf("execute agent inline: %w", err)
        }

        return ConvertRunOutputToToolResult(call.Name, output), nil
    }
}

// PayloadToString converts a tool payload to string for agent consumption.
func PayloadToString(payload any) string {
    if str, ok := payload.(string); ok {
        return str
    }
    if payload == nil {
        return ""
    }
    payloadBytes, _ := json.Marshal(payload)
    return string(payloadBytes)
}
```

Engine Adapter Requirement

- To enable nested agent execution inside activities, engine adapters MUST inject the current `WorkflowContext` into the activity handler `context.Context` before invoking the registered handler. Use:

```go
ctxForHandler := engine.WithWorkflowContext(activityCtx, wfCtx)
out, err := activityDef.Handler(ctxForHandler, input)
```

- Runtime helpers (e.g., default agent-tool execute) retrieve the workflow context with `engine.WorkflowContextFromContext(ctx)`. Without this injection, nested `ExecuteAgentInline` will fail to find a workflow context.

**Key Design Principles**:
- ✅ **Codegen generates data, not logic** - Constants + thin wrapper
- ✅ **Logic lives in runtime** - Fix bugs once, all agents benefit
- ✅ **Ergonomic configuration** - NewRegistration + WithText/WithTemplate options; full override via custom Execute
- ✅ **Type-safe customization** - Generated tool name constants prevent typos
- ✅ **No context pollution** - Run metadata passed explicitly via ToolCallRequest fields (RunID, SessionID, TurnID)
- ✅ **Minimal generation** - ~15 lines per exported agent vs. 40+ with inline Execute

**User Code Examples**:

```go
// Tier 1: Simple case (99% of users)
reg, _ := atlasread.NewRegistration(rt, "")
rt.RegisterToolset(reg)

// Tier 2: Common case - agent-wide system prompt + per-tool templates
tmpl, _ := runtime.CompileAgentToolTemplates(map[tools.ID]string{
    atlasread.ToolQueryData:   "Query: {{ tojson . }}",
    atlasread.ToolAnalyzeData: "Analyze: {{ tojson . }}",
}, nil)
reg, _ := atlasread.NewRegistration(rt, "You are a data expert.",
    atlasread.WithTemplate(atlasread.ToolQueryData, tmpl[atlasread.ToolQueryData]),
    atlasread.WithTemplate(atlasread.ToolAnalyzeData, tmpl[atlasread.ToolAnalyzeData]),
)
rt.RegisterToolset(reg)

// Tier 3: Power user - full customization
reg, _ := atlasread.NewRegistration(rt, "")
reg.Execute = func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error) {
    // Completely custom execution logic
    // - Custom payload-to-message conversion
    // - Conditional logic per tool
    // - Additional validation
    // - Custom error handling
    // Still has access to runtime.NestedRunID, runtime.PayloadToString, etc.
    wfCtx := engine.WorkflowContextFromContext(ctx)
    
    messages := buildCustomMessages(call) // User-defined
    nestedRunCtx := run.Context{
        RunID:     runtime.NestedRunID(call.RunID, call.Name),
        SessionID: call.SessionID,
        TurnID:    call.TurnID,
    }
    
    output, err := rt.ExecuteAgentInline(wfCtx, atlasread.AgentID, messages, nestedRunCtx)
    if err != nil {
        return planner.ToolResult{}, err
    }
    return runtime.ConvertRunOutputToToolResult(call.Name, output), nil
}
rt.RegisterToolset(reg)
```

### Canonical Example: Chat Data Loop (Temporal + MCP)

This example wires a real workflow engine (Temporal), the MCP helper, streaming, and durable memory into a single end-to-end loop for the chat agent. Use it as the reference integration.

Prerequisites

- Temporal cluster and client connection
- MongoDB (or your chosen durable store) for memory and run metadata
- MCP helper/caller implementation (HTTP/stdio/SSE supported)
- Pulse client for publishing runtime events and an SSE server/subscriber for UI clients

Engine Adapter Requirement

- The Temporal adapter MUST inject the current `WorkflowContext` into the activity handler context before invocation so nested agent execution works:

```go
ctxForHandler := engine.WithWorkflowContext(activityCtx, wfCtx)
_, err := activityDef.Handler(ctxForHandler, input)
```

Wiring

```go
// Build Temporal engine adapter (sketch)
temporalEng, err := temporal.New(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:  "127.0.0.1:7233",
        Namespace: "default",
    },
    WorkerOptions: temporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
})
if err != nil { log.Fatal(err) }
defer temporalEng.Close()

// Durable stores and Pulse-backed streaming sink (SSE subscribers consume Pulse)
memStore := memorymongo.New(mongoClient)
runStore := runmongo.New(mongoClient)
pl       := /* construct Pulse client (e.g., Redis-backed) */
sink, err := pulse.NewSink(pulse.Options{Client: pl})
if err != nil { log.Fatal(err) }

// Runtime
rt := runtime.New(runtime.Options{
    Engine:      temporalEng,
    MemoryStore: memStore,
    RunStore:    runStore,
    Stream:      sink,
})

// MCP toolset registration
caller := mcp.NewCaller(mcpClient) // choose http/stdio/sse callers
if err := assistant.RegisterAssistantMcpToolset(ctx, rt, caller); err != nil {
    log.Fatal(err)
}

// Chat agent registration
if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: newChatPlanner()}); err != nil {
    log.Fatal(err)
}

// Optional: per-tool prompts for exported agent-tools (typed tool IDs)
reg, _ := assistanttools.NewRegistration(rt, "",
    assistanttools.WithTextAll(assistanttools.ToolIDs, "Handle: {{ . }}"),
)
custom := runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
    AgentID:       assistanttools.AgentID,
    Name:          assistanttools.Name,
    TaskQueue:     "assistant.tools",
    SystemPrompts: map[tools.ID]string{assistanttools.QueryData: "You are a data expert."},
})
if err := rt.RegisterToolset(custom); err != nil { log.Fatal(err) }

// Start Temporal worker
if err := temporalEng.Worker().Start(); err != nil { log.Fatal(err) }

// Launch a run
input := runtime.RunInput{
    AgentID:   chat.AgentID,
    RunID:     "chat-001",
    SessionID: "session-abc",
    TurnID:    "turn-1",
    Messages: []planner.AgentMessage{{Role: "user", Content: "What changed last week?"}},
}
handle, err := rt.StartRun(ctx, input)
if err != nil { log.Fatal(err) }

var out runtime.RunOutput
if err := handle.Wait(ctx, &out); err != nil { log.Fatal(err) }
```

What You Get

- Streaming: events are published to Pulse via the sink; your SSE server subscribes and streams `ToolCallScheduled`, `ToolResultReceived`, planner notes, and the final assistant message to clients in real time
- Memory: when `MemoryStore` is set, hook subscribers persist transcript events (tool calls/results, notes, assistant messages) keyed by `RunID`
- Policy: the runtime enforces caps/time budgets per turn
- Agent-as-tool: exported agents run inline via `ExecuteAgentInline`; per-tool prompts via typed `tools.ID` keys

Adapter Checklist

- [ ] Inject `WorkflowContext` into activity handler context
- [ ] Respect activity options (queue, timeout, retry policy)
- [ ] Deliver `ActivityRequest.Input` as-is to the handler
- [ ] Support async path returning `Future` semantics

#### Step 3A: Generate ExecuteToolActivity That Calls Toolset.Execute

**Goal**: Generate activity handlers that dispatch to the registered toolset's Execute function.

**Generate `ExecuteToolActivity` per-agent** (in `agents/codegen/templates/activities.go.tpl`):

Each agent gets a generated activity that looks up and calls the toolset's Execute function:

```go
// {{ .Agent.GoName }}ExecuteToolActivity executes a tool for the {{ .Agent.Name }} agent.
// It looks up the registered toolset and calls its Execute function.
func {{ .Agent.GoName }}ExecuteToolActivity(
    ctx context.Context,
    input runtime.ToolInput,
) (runtime.ToolOutput, error) {
    // Get runtime instance (injected during registration)
    rt := getRuntimeFromContext(ctx)
    
    // Look up the toolset registration for this tool
    toolset, ok := rt.LookupToolset(input.ToolsetName)
    if !ok {
        return runtime.ToolOutput{}, fmt.Errorf("toolset not registered: %s", input.ToolsetName)
    }
    
    // Call the toolset's Execute function (provided by user or generated)
    result, err := toolset.Execute(ctx, input.ToolName, input.Payload)
    if err != nil {
        return runtime.ToolOutput{}, err
    }
    
    return runtime.ToolOutput{Payload: result}, nil
}

```

**Key points**:
- Generated **per-agent** (not a Runtime method)
- Activity is simple: lookup toolset, call Execute
- ALL complexity is in the toolset registration's Execute function
- For agent-tools, codegen generates the Execute function that calls `ExecuteAgentInline`
- For service-based tools, codegen generates the Execute function that calls service clients
- For custom tools, users provide their own Execute function

**Workflow code stays uniform** (in `runtime/workflow.go`):
```go
func (r *Runtime) executeToolCalls(...) ([]planner.ToolResult, error) {
    futures := make([]futureInfo, 0, len(calls))
    
    for _, call := range calls {
        // Always execute via activity - no conditionals!
        future, err := wfCtx.ExecuteActivityAsync(ctx, engine.ActivityRequest{
            Name:  activityName,  // Same for all tools
            Queue: call.Queue,
            Input: ToolInput{
                RunID:       runID,
                AgentID:     agentID,
                ToolsetName: call.ToolsetName,
                ToolName:    call.Name,
                Payload:     call.Payload,
            },
        })
        futures = append(futures, futureInfo{future: future, call: call})
    }
    
    // Collect results...
}
```

**Key insight**: 
- Workflow is uniform (always uses activities)
- Activity is uniform (always calls toolset.Execute)
- Dispatch happens in the toolset registration's Execute function
- For agent-tools, codegen GENERATES the Execute function
- Zero runtime type detection!

#### Step 3B: Generate Agent-Tool Execute Functions

**Goal**: For agents with `Exports` blocks, generate a toolset registration helper that provides an Execute function calling `ExecuteAgentInline`.

**Generate in `gen/<service>/agents/<agent>/agent_toolset.go`**:

```go
// NewAtlasDataAgentToolsetRegistration creates a toolset registration for the
// exported tools of the atlas_data_agent agent. This registration's Execute
// function calls ExecuteAgentInline to run the agent's planning loop inline.
func NewAtlasDataAgentToolsetRegistration(rt *Runtime) runtime.ToolsetRegistration {
    return runtime.ToolsetRegistration{
        Name: "atlas.read",
        Execute: func(ctx context.Context, toolName string, payload json.RawMessage) (json.RawMessage, error) {
            // Convert tool payload to agent messages
            messages, err := convertToolPayloadToMessages(toolName, payload)
            if err != nil {
                return nil, fmt.Errorf("convert payload: %w", err)
            }
            
            // Build nested run context
            wfCtx := workflow.GetContext(ctx)
            nestedRunCtx := run.Context{
                RunID:            generateNestedRunID(wfCtx, toolName),
                SessionID:        getSessionIDFromContext(ctx),
                TurnID:           getTurnIDFromContext(ctx),
                ParentToolCallID: getToolCallIDFromContext(ctx),
            }
            
            // Execute the agent inline
            output, err := rt.ExecuteAgentInline(
                wfCtx,
                "atlas.atlas_data_agent",  // Fully qualified agent ID
                messages,
                nestedRunCtx,
            )
            if err != nil {
                return nil, fmt.Errorf("execute agent inline: %w", err)
            }
            
            // Convert agent output to JSON
            return json.Marshal(output.Final)
        },
    }
}
```

**Key points**:
- Generated ONLY for agents with `Exports` blocks
- Returns a `ToolsetRegistration` with pre-wired Execute function
- Execute function calls `ExecuteAgentInline` (which we'll implement in Step 1)
- User registers it: `rt.RegisterToolset("atlas.read", NewAtlasDataAgentToolsetRegistration(rt))`

#### Step 4: Extend `runLoop` for Child Tracking

**Goal**: Allow `runLoop` to accept an optional parent tracker and update it during execution.

Just pass tracker as optional param to existing `runLoop`:
```go
func (r *Runtime) runLoop(
    wfCtx engine.WorkflowContext,
    reg AgentRegistration,
    base planner.PlanInput,
    initial planner.PlanResult,
    caps policy.CapsState,
    deadline time.Time,
    nextAttempt int,
    seq *turnSequencer,
    parentTracker *childTracker,  // NEW: optional (nil for top-level runs)
) (RunOutput, error) {
    // ... implementation stays the same, just add tracker logic ...
}
```

#### Step 5: Helper Functions

**Goal**: Add utility functions for ID generation and payload conversion.

**Add to `runtime/helpers.go`**:
```go
// generateToolCallID creates a unique, deterministic ID for a tool call.
// Uses workflow's built-in deterministic ID generation when available.
func generateToolCallID(call planner.ToolCallRequest) string {
    // For now, use a simple counter-based approach
    // TODO: Use workflow-deterministic UUID generation
    return fmt.Sprintf("%s_%d", call.Name, time.Now().UnixNano())
}

// generateNestedRunID creates a unique run ID for a nested agent invocation.
// The run ID includes the parent's run ID and tool name for traceability.
func generateNestedRunID(wfCtx engine.WorkflowContext, toolName string) string {
    parentRunID := wfCtx.RunID()
    // TODO: Use workflow-deterministic UUID generation for replay safety
    return fmt.Sprintf("%s/nested/%s/%d", parentRunID, toolName, time.Now().UnixNano())
}

// convertToolPayloadToMessages converts a tool call payload (JSON) into agent messages.
// The default implementation treats the payload as a user message content.
func convertToolPayloadToMessages(spec *tools.ToolSpec, payload json.RawMessage) ([]planner.AgentMessage, error) {
    // Decode the payload using the tool's codec
    var decoded any
    if err := spec.PayloadCodec.Unmarshal(payload, &decoded); err != nil {
        return nil, fmt.Errorf("unmarshal payload: %w", err)
    }
    
    // Format as a user message
    // TODO: Support richer formatting (e.g., structured fields)
    content := fmt.Sprintf("%v", decoded)
    return []planner.AgentMessage{
        {
            Role:    "user",
            Content: content,
        },
    }, nil
}

// convertAgentOutputToToolOutput converts agent execution output into tool output format.
func convertAgentOutputToToolOutput(output RunOutput) (ToolOutput, error) {
    // Encode the final message as JSON
    payload, err := json.Marshal(output.Final)
    if err != nil {
        return ToolOutput{}, fmt.Errorf("marshal output: %w", err)
    }
    
    return ToolOutput{
        Payload: payload,
    }, nil
}
```

#### Step 6: Testing Strategy

**Unit tests**:
1. **Test `childTracker` methods**:
   - `registerDiscovered` with empty, single, multiple IDs
   - `needsUpdate` after discovery and after `markUpdated`
   - Idempotent discovery (same ID added twice)

2. **Test `ExecuteAgentInline` API**:
   - Accepts messages and returns agent output
   - Creates nested run context with correct parent tracking
   - Calls runLoop with proper parameters
   - Handles planner errors correctly

3. **Test tool spec metadata**:
   - Tool specs have `IsAgentTool` flag set correctly
   - Agent-tools include `AgentID` field
   - Regular tools have `IsAgentTool=false`
   - Codegen sets flags based on `Exports` blocks

4. **Test `executeToolCalls` dispatch logic**:
   - Checks tool spec `IsAgentTool` flag
   - Calls `executeAgentToolInline` for agent-tools
   - Calls `ExecuteActivityAsync` for regular tools
   - No runtime type detection or string matching

**Integration tests**:
1. **Agent-as-tool execution**:
   - Register agent with `Exports` block
   - Parent agent calls agent-tool
   - Verify nested agent executes inline
   - Verify no separate workflow spawned

2. **Child discovery tracking**:
   - Nested agent's planner discovers tools progressively
   - First iteration returns 2 tools, second returns 3 more
   - Verify `ToolCallUpdatedEvent` emitted with correct counts
   - Verify update event references parent's tool call ID

3. **Event sequencing**:
   - Verify events from nested agent use parent's sequencer
   - Verify turn IDs propagate correctly
   - Verify event ordering is deterministic

**Example test** (in `runtime_test.go`):
```go
func TestAgentAsToolDirectLoop(t *testing.T) {
    // Mock nested agent planner that discovers tools
    nestedPlanner := &mockPlanner{
        iterations: []planner.PlanResult{
            {ToolCalls: []planner.ToolCallRequest{{Name: "subtool1"}, {Name: "subtool2"}}},
            {ToolCalls: []planner.ToolCallRequest{{Name: "subtool3"}}},
            {FinalResponse: &planner.FinalResponse{Message: planner.Message{Content: "nested done"}}},
        },
    }
    
    // Track emitted events
    var events []hooks.Event
    subscriber := hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
        events = append(events, evt)
        return nil
    })
    
    bus := hooks.NewBus()
    bus.Register(subscriber)
    
    rt := NewRuntime(Options{Hooks: bus})
    
    // Register nested agent
    rt.RegisterAgent("nested", AgentRegistration{
        AgentID:      "nested",
        Planner:      nestedPlanner,
        ExportsTools: true,
    })
    
    // Register parent agent that calls nested agent
    parentPlanner := &mockPlanner{
        iterations: []planner.PlanResult{
            {ToolCalls: []planner.ToolCallRequest{{Name: "nested.method"}}},
            {FinalResponse: &planner.FinalResponse{Message: planner.Message{Content: "parent done"}}},
        },
    }
    rt.RegisterAgent("parent", AgentRegistration{
        AgentID: "parent",
        Planner: parentPlanner,
    })
    
    // Execute parent workflow
    input := RunInput{
        AgentID: "parent",
        TurnID:  "turn-1",
    }
    
    _, err := rt.ExecuteWorkflow(mockWorkflowContext, input)
    require.NoError(t, err)
    
    // Verify child discovery events were emitted
    updates := filterEvents[*hooks.ToolCallUpdatedEvent](events)
    require.Len(t, updates, 2, "nested agent should emit 2 update events")
    
    // Verify first update: 2 child tools discovered
    assert.Equal(t, 2, updates[0].ExpectedChildrenTotal)
    
    // Verify second update: 3 child tools total
    assert.Equal(t, 3, updates[1].ExpectedChildrenTotal)
    
    // Verify nested tool calls were scheduled
    scheduled := filterEvents[*hooks.ToolCallScheduledEvent](events)
    nestedTools := filter(scheduled, func(e *hooks.ToolCallScheduledEvent) bool {
        return strings.HasPrefix(e.ToolName, "subtool")
    })
    assert.Len(t, nestedTools, 3, "should schedule 3 nested tools")
}
```

### Architecture Benefits

| Aspect | Traditional (Separate Workflows) | Toolset Registration (This Approach) |
|--------|----------------------------------|--------------------------------------|
| **Execution model** | Start child workflow, wait for completion | Direct function call via toolset.Execute |
| **Type detection** | Runtime type checking (`isAgentTool()`) | No detection - codegen provides Execute function |
| **Dispatch location** | In workflow or activity code | In toolset registration (codegen or user-provided) |
| **Determinism** | Requires workflow coordination | Natural - same execution context |
| **Code complexity** | Workflow spawning + result marshaling | Simple function composition |
| **Testing** | Must mock workflow engine extensively | Can test Execute functions directly |
| **Performance** | Workflow startup overhead | Zero overhead (inline execution) |
| **Event ordering** | Requires event synchronization | Natural - sequential execution |
| **Child tracking** | Via workflow signals/queries | Via function call stack + tracker |
| **Composability** | Separate execution units | Natural function composition |
| **Extensibility** | Hard to add new tool types | Easy - just generate different Execute functions |
| **User control** | Framework dictates tool implementation | Users provide Execute for custom tools |

**Key advantages**:
1. ✅ **Uniform workflow**: Always calls `ExecuteActivityAsync` - ZERO conditionals
2. ✅ **Uniform activity**: Always calls `toolset.Execute` - ZERO dispatch logic
3. ✅ **Codegen-driven**: Execute functions are generated for service/agent tools
4. ✅ **User-extensible**: Users provide Execute for custom/server-side tools
5. ✅ **More testable**: Can test Execute functions independently
6. ✅ **Better determinism**: Workflow replay "just works" (same execution path)
7. ✅ **Natural semantics**: Agent-as-tool is just another Execute function
8. ✅ **Zero overhead**: No workflow spawning (inline execution via ExecuteAgentInline)
9. ✅ **Clean separation**: Workflow/activity don't know about tool types
10. ✅ **Distributable**: Activities can run on different workers if needed

**Design note**:
- **Workflow** → Always calls `ExecuteActivityAsync` (no dispatch)
- **Activity** → Always calls `toolset.Execute` (no dispatch)
- **Toolset Registration** → Execute function does the dispatch
- For agent-tools: codegen generates Execute that calls `ExecuteAgentInline`
- For service-based tools: codegen generates Execute that calls service clients
- For custom tools: user provides Execute with their implementation
- This is the most natural semantic: a tool call completes and returns a result

### File Organization

```
agents/runtime/runtime/
  runtime.go          - Runtime struct with toolsets map
                        RegisterToolset, LookupToolset methods
                        ExecuteAgentInline public API (NEW)
  
  workflow.go         - executeToolCalls (uniform, always uses activities)
                        childTracker struct and methods
                        runLoop (accepts optional parentTracker)
  
  activities.go       - Planner activities (PlanStart, PlanResume)
  
  helpers.go          - generateToolCallID
                        generateNestedRunID
                        convertToolPayloadToMessages
                        convertAgentOutputToToolOutput
                        (NEW helper functions)
  
  types.go            - ToolsetRegistration struct (NEW)
                        ToolInput (with ToolsetName field) (UPDATED)
                        ToolOutput (already exists)

agents/runtime/tools/
  tools.go            - ToolSpec (already has IsAgentTool and AgentID fields)

agents/codegen/
  data.go             - ToolData (already has IsExportedByAgent flag)
  
  templates/
    tool_spec.go.tpl  - Sets IsAgentTool flag from DSL (ALREADY DONE)
    
    activities.go.tpl - Generates ExecuteToolActivity (UPDATED)
                        Calls rt.LookupToolset and toolset.Execute
    
    agent_toolset.go.tpl - Generates NewAgentToolsetRegistration (NEW)
                           Only for agents with Exports blocks
                           Returns ToolsetRegistration with Execute
                           that calls rt.ExecuteAgentInline
    
    service_toolset.go.tpl - Generates NewServiceToolsetRegistration (NEW)
                             For service-based toolsets
                             Returns ToolsetRegistration with Execute
                             that calls service clients

gen/<service>/agents/<agent>/
  activities.go       - Generated: ExecuteToolActivity
                        Simple: calls rt.LookupToolset and toolset.Execute
  
  agent_toolset.go    - Generated (only for agents with Exports):
                        NewAgentToolsetRegistration helper
  
  service_toolset.go  - Generated (for service-based toolsets):
                        NewServiceToolsetRegistration helper

agents/runtime/hooks/
  events.go           - ToolCallUpdatedEvent (ALREADY EXISTS)
  hooks.go            - ToolCallUpdated EventType (ALREADY EXISTS)
```

**Design notes**:
- **Uniform workflow execution** - always calls `ExecuteActivityAsync`, zero conditionals
- **Uniform activity implementation** - always calls `toolset.Execute`
- **Dispatch via registration** - Execute function does the dispatch (codegen-provided or user-provided)
- **Three types of toolsets**:
  1. Service-based → codegen generates Execute that calls service clients
  2. Agent-tools → codegen generates Execute that calls ExecuteAgentInline
  3. Custom/server-side → user provides Execute function
- **Future tool types** (MCP, HTTP) → Generate Execute functions that call appropriate handlers
- **No runtime type detection or string matching!**

### Migration Path (for AURA)

When migrating AURA's ADA agent:

1. **Define ADA in DSL**:
```go
Agent("atlas_data_agent", func() {
    Exports(func() {
        Toolset("atlas.read", func() {
            Tool("GetKeyEvents", func() {
                Args(func() { /* ... */ })
                Return(func() { /* ... */ })
            })
            Tool("AnalyzeSensorPatterns", func() {
                Args(func() { /* ... */ })
                Return(func() { /* ... */ })
            })
        })
    })
    RunPolicy(func() {
        MaxToolCalls(20)
        TimeBudget("5m")
    })
})
```

2. **Generate code**: `goa gen` produces direct loop wrappers
3. **Implement planner**: Port ADA's prompt/planning logic to `planner.Planner`
4. **Register with runtime**: 
   ```go
   rt.RegisterAgent("atlas_data_agent", AgentRegistration{
       AgentID:      "atlas_data_agent",
       Planner:      adaPlanner,
       ExportsTools: true,
   })
   ```
5. **Test**: Verify child discovery matches AURA's `adaLoop` behavior

**Key difference from AURA**:
- AURA: `adaLoop` starts separate workflow per invocation
- Goa-AI: Direct `runLoop` invocation - simpler, more composable
- Same observable behavior: child discovery, progress tracking, events

### Success Criteria

**Infrastructure** (Already Complete):
- [x] `ToolSpec` includes `IsAgentTool` and `AgentID` fields
- [x] Codegen sets `IsAgentTool=true` for tools in `Exports` blocks  
- [x] `ToolCallUpdatedEvent` and event stamping infrastructure
- [x] `childTracker` structure and methods
- [x] Parent-child tracking fields in events

**Agent-as-Tool Implementation** (To Be Implemented):
- [ ] `ExecuteAgentInline` public API defined in `runtime.go`
- [ ] `ToolsetRegistration` type with Execute function field
- [ ] `Runtime.RegisterToolset` and `Runtime.LookupToolset` methods
- [ ] Codegen generates `ExecuteToolActivity` that calls `toolset.Execute`
- [ ] Codegen generates `NewAgentToolsetRegistration` for `Exports` agents
- [ ] Generated agent-tool Execute function calls `ExecuteAgentInline`
- [ ] `executeToolCalls` is uniform (always calls `ExecuteActivityAsync`)
- [ ] Zero conditionals in workflow code (no IsAgentTool checks at runtime)
- [ ] `ToolInput` includes `ToolsetName` field
- [ ] `runLoop` accepts optional `parentTracker` parameter
- [ ] `childTracker` accumulates discoveries during nested execution
- [ ] Helper functions for ID generation and payload conversion
- [ ] `convertToolPayloadToMessages` and `convertAgentOutputToToolOutput`

**Testing**:
- [ ] Unit tests for `childTracker` methods
- [ ] Unit tests for `ExecuteAgentInline` API
- [ ] Integration test: agent-as-tool execution (no separate workflow)
- [ ] Integration test: child discovery across multiple planner iterations
- [ ] Integration test: `ToolCallUpdatedEvent` emitted with correct counts
- [ ] Integration test: event sequencing for nested agents

**Documentation**:
- [ ] Document toolset registration pattern
- [ ] Document when to use `ExecuteAgentInline` vs `runLoop`  
- [ ] Document codegen for agent-tools
- [ ] Update AURA migration guide with agent-as-tool patterns

**Hook/Event Parity**:
- [x] Extend `ToolResultReceivedEvent` (and the memory subscriber payloads derived from it) with `ToolCallID` and `ParentToolCallID` so downstream services can correlate tool results with the originating call hierarchy.

### References

- AURA implementation: `~/src/aura/services/orchestrator/workflows/ada_loop.go`
- Key methods: `registerDiscovered`, `updateParentExpectation`
- Event types: `SessionToolCallUpdateEvent`, `ToolCallEvent` with `ExpectedChildrenTotal`
- Design philosophy: Composition over coordination

**Phase 4: Feature Modules**
- Pulse: `features/stream/pulse` replaces `services/*/clients/pulse`
- Mongo: `features/memory/mongo` + `features/run/mongo` replace `services/session`
- Models: `features/model/bedrock` wraps existing inference-engine integration

**Phase 5: Policy & Caps**
- Port orchestrator's cap enforcement to `policy.Engine` implementation
- Migrate retry hint logic to goa-ai's `RetryHint` enum

### Expected Code Savings

Based on AURA's current structure:

| Component | Current Lines | Goa-AI Lines | Savings |
|-----------|---------------|--------------|---------|
| Workflow boilerplate | ~600 | ~50 (generated) | 92% |
| Tool registration | ~400 | ~20 (generated) | 95% |
| Event publishing | ~800 | ~100 (hooks) | 88% |
| Memory persistence | ~500 | ~50 (subscribers) | 90% |
| Turn sequencing | ~100 | ~20 (built-in) | 80% |
| **Total** | **~2,400** | **~240** | **90%** |

### Validation Checklist

Before declaring migration complete, verify:

- [ ] ADA methods can dynamically discover arbitrary child tools
- [ ] Parent's `ExpectedChildrenTotal` updates as children discovered
- [ ] UI can reconstruct parent-child tree from events
- [ ] Turn sequences are monotonic and gaps are detectable
- [ ] Parallel tool execution works across task queues
- [ ] Tool caps enforce correctly (max calls, consecutive failures)
- [ ] Pulse events maintain exact JSON schema compatibility
- [ ] Session timeline events have identical structure
- [ ] Temporal workflow replay is deterministic

This concrete alignment ensures goa-ai isn't just theoretically compatible with AURA—it directly implements the proven patterns already in production.
