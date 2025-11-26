Title: Tools as First‑Class Citizens in goa‑ai — Ideal State and Plan

## Overview

- **Purpose**: Reframe goa‑ai around *tools and toolsets* as the primary abstraction, with agents and MCP as *consumers and exposures* of those tools. The goal is a clean, elegant model where services define and own tools, and all other pieces (agents, MCP servers, planners, UIs) sit on top of that foundation.
- **Scope**: DSL, expressions, codegen layout, runtime contracts, and repository migration. We explicitly **do not optimize for backward compatibility**; we want the best possible end state and are willing to break existing APIs in this repository.
- **Context**: Builds on existing work captured in `agent_as_tool_ideal.md` and `unified_tooling_plan.md`, but takes one step higher to design a *tool‑first architecture* instead of an agent‑centric one.

## Desired Outcome

- **Tools and toolsets are first‑class**:
  - The core mental model is: “Services define toolsets; agents and MCP consume/expose them.”
  - Toolsets exist independently of agents; agents no longer feel like the “host” for tools.
- **Canonical specs per tool provider**:
  - Each exported toolset has a single, canonical spec/types/codecs package anchored at its provider (a Goa service).
  - All consumers (agents, MCP, external clients) import the same canonical package.
- **Unified export semantics**:
  - Services and agents both export toolsets; “export” means “this toolset is part of my public tool API,” regardless of implementation.
- **Simple tool identity**:
  - Tool IDs are simple, globally unique names (e.g., `update_todos`, `plan_session`), enforced at design time.
  - Toolset names are globally unique (e.g., `todos`, `planning`), enabling a neat `gen/<service>/tools/<toolset>` layout.
- **Simple, orthogonal components**:
  - DSL: define toolsets, export toolsets, use toolsets.
  - Codegen: provider‑anchored specs + consumer‑side helpers.
  - Runtime: registration + execution + streaming, independent of where tools are defined.
- **Elegant UX for non‑agent services**:
  - A plain Goa service can define and export toolsets without introducing any agents.
  - Adding an agent on top becomes an incremental choice, not a prerequisite for tool exposure.

## Ideal State: Concepts and Responsibilities

### Core Concepts

- **Tool**:
  - A named operation with input and output schemas.
  - Implementation is either:
    - **Method‑backed**: bound to a Goa service method via `BindTo` or method annotation, or
    - **Executor‑backed**: implemented by a custom executor, a remote agent, or an MCP caller.
- **Toolset**:
  - A named group of tools defined in the DSL.
  - Has an owning Goa service and a description.
  - Exists independently of agents; can be defined at the top level or within a service/agent.
- **Provider**:
  - The Goa **service** that owns an exported toolset (e.g., `todos` for `TodosToolset`).
  - Agents running under a service can implement tools for that service’s toolsets (implementation kind = agent), but ownership remains with the service.
- **Consumer**:
  - Any component that *uses* a toolset:
    - Agents via `Uses` / `Toolset` / `AgentToolset`,
    - MCP servers, external clients, or even other services.

### Export Semantics

- **Service‑level exports**:
  - Services export toolsets directly by declaring toolsets and binding tools to methods as needed.
  - In practice, ownership is inferred from bindings (e.g., `BindTo("todos", "update_todos")` for a `todos` toolset).
- **Agent‑level exports**:
  - Agents still support `Exports`, but the semantics are: “this agent implements tools for a toolset owned by its service.”
  - Example: a planning agent exporting higher‑level orchestration tools built on top of service tools.
- **Identity and provenance**:
  - Tool IDs are simple names (e.g., `update_todos`, `plan_session`) and are globally unique.
  - Tool provenance (which service/agent owns/implements it) is separate metadata:
    - `OwnerService` (the Goa service),
    - `ImplementationKind` (method, agent, MCP, external),
    - optional `AgentName` or MCP suite.

## Ideal State: DSL and Expressions

### Toolset and Tool DSL

Defining and binding tools remains idiomatic Goa:

```go
var TodosToolset = Toolset("todos", func() {
    ToolsetDescription("Update or fetch minimal todos for a run.")

    Tool("update_todos", "Create/replace or merge todos for a run", func() {
        Args(TodosUpdateInput)     // or Args(func() { ... })
        Return(TodosSnapshot)
        BindTo("update_todos")     // bind to the todos.update_todos method
    })

    Tool("get_todos", "Get todos snapshot for a run", func() {
        Args(TodosGetInput)
        Return(TodosSnapshot)
        BindTo("get_todos")
    })
})
```

Key properties:

- `Toolset` is a first‑class construct that can be reused across agents and MCP; it does not depend on an `Agent`.
- `BindTo` connects tools to methods but does not change their identity or schema.
- The owning service for a toolset is derived from:
  - The service where it is used/bound (method‑backed), or
  - The service of the agent that exports/uses it (agent‑backed).

### Agent‑Level Tool DSL

Agents use and optionally export toolsets:

```go
var _ = Service("planner", func() {
    Agent("session-planner", "Plan tasks for a session", func() {
        Use(
            Toolset(TodosToolset) // consumes TodosToolset owned by the todos service
        })

        Export(
            Toolset("planning", func() {
                Tool("plan_session", "Create a session plan", func() {
                    Args(SessionPlanInput)
                    Return(SessionPlanResult)
                    // Implementation is agent‑backed; no BindTo
                })
            })
        })

        RunPolicy(func() { /* ... */ })
    })
})
```

Key properties:

- `Uses` and `AgentToolset` reference toolsets defined in services or other agents.
- Agents exporting toolsets are just one implementation strategy; ownership remains with the service.
- The same `ToolsetExpr` structure backs both service‑bound and agent‑implemented toolsets.

### MCP Tool DSL Integration

Method‑level tool declarations integrate with the same tool model:

```go
Service("calculator", func() {
    MCPServer("calc", "1.0.0")

    Method("add", func() {
        Payload(AddInput)
        Result(AddResult)

        Tool("add", "Add two numbers")  // method-level MCP tool
    })
})
```

Ideal behavior:

- `Tool("add", "Add two numbers")` on a method:
  - Associates the method with a service‑owned tool (implicitly part of a toolset).
  - Reuses the same schemas that a toolset tool would use.
- `MCPServer` is a way to **expose** existing tools via MCP; it does not create a separate “MCP tools world”.

### Expressions and Invariants

At the expression layer:

- `ToolsetExpr` has:
  - `Name` (globally unique across defining toolsets),
  - `Origin` (for references),
  - `OwnerService` (*optional* pointer to the owning service),
  - `Tools` (the tool expressions).
- `ToolExpr` has:
  - Link to the owning `ToolsetExpr`,
  - Optional method binding (service/method names),
  - Schema attributes (`Args`, `Return`).

Validation ensures:

- Defining toolsets (those with `Origin == nil`) have **globally unique** names.
- Tools have unique names **within a toolset**.
- Tool names are also enforced to be globally unique so `tools.Ident` can stay a plain name without prefixes.

## Ideal State: Codegen Layout

### Provider‑Anchored Specs

For each exported toolset, codegen emits a canonical package under the owning service:

- `gen/<service>/tools/<toolset>/types.go`
- `gen/<service>/tools/<toolset>/codecs.go`
- `gen/<service>/tools/<toolset>/specs.go`
- (Optional) `gen/<service>/tools/<toolset>/transforms.go` for method‑backed tools.

These packages:

- Define the public Go types used for tool payloads/results.
- Host the generated codecs and `tool_specs.Specs` map.
- Provide helper functions (e.g., `Specs`, `AdvertisedSpecs`) for planners and UIs.

### Consumers (Agents, MCP, Clients)

For each consumer, codegen emits thin wrappers that depend only on provider specs:

- **Agent consumers**:
  - Typed call builders: `New<Tool>Call(*<Tool>Payload, ...CallOption) planner.ToolRequest`.
  - Executor factories for method‑backed tools that wrap service clients.
  - Agent‑as‑tool executors for tools implemented by agents.
- **MCP executors**:
  - `New<Provider><Toolset>MCPExecutor(caller)` that uses provider codecs and specs.
- **Agent‑as‑tool consumers**:
  - `New<Agent><Toolset>ToolsetRegistration(...)` wiring planner/runtime to provider specs.

Aggregated specs per agent:

- `gen/<service>/agents/<agent>/specs/specs.go` imports provider specs and aggregates them into a view the agent passes to the model. It does not emit new schemas.

## Ideal State: Runtime and Planner Behavior

### Registration and Execution

- **Registration**:
  - Each provider‑side toolset gets a generated registration helper that:
    - Registers tools with `tools.Ident` based on their simple, globally unique names.
    - Carries `ToolSpec` entries (service, toolset, schemas, hints, tags).
    - Hooks in adapters, telemetry, JSON‑only flags, etc., as described in `unified_tooling_plan.md`.
- **Execution**:
  - Planners construct tool calls using typed builders and tool IDs.
  - Runtime routes calls to the appropriate executor (service client, agent‑as‑tool, MCP, custom).
  - All executors use provider specs for encode/decode and share the same runtime contracts (retry hints, telemetry, result envelopes).

### Streaming and Events

- Tool calls emit a consistent set of planner events (`ToolStarted`, `ToolResult`, `ToolError`, etc.).
- Service-backed, agent-backed, and MCP-backed tools all:
  - Use the same tool IDs,
  - Share the same result envelope,
  - Integrate with the same telemetry and policy machinery.

#### Agent-as-Tool Event Propagation

- Inline toolsets (agent-as-tool) publish `ToolCallScheduled` immediately before the child agent starts and `ToolResultReceived` as soon as the child finishes. Both events carry the **parent** run ID/agent ID/tool metadata, so sinks/UI never have to reason about nested run IDs.
- The provider agent still emits its own hook events under its child run for debugging/telemetry, but sinks that only register the parent run simply ignore them.
- This keeps the streaming surface area small: one planner request → one tool start/result pair, regardless of whether the implementation is a service call or a nested agent.

## Concrete Ideal Example: `todos` Service and a Planner Agent

### Service: Define and Bind Tools

- Service `todos` defines `TodosToolset` and binds tools to methods.
- Codegen outputs:
  - `gen/todos/tools/todos/{types.go,codecs.go,specs.go,transforms.go}`
  - A helper `NewTodosToolsetRegistration(rt, exec, opts...)`.

### Agent: Consume Service Tools

- Agent `planner.session-planner` declares `Use(TodosToolset)`.
- Codegen:
  - Imports `gen/todos/tools/todos/specs`.
  - Emits typed builders for `update_todos` and `get_todos`.
  - Emits a default service executor factory that wraps the generated `todos` client.

### MCP: Expose Tools

- MCP server for `todos` simply points at `TodosToolset` specs:
  - `MCPServer("todos", "1.0.0")` plus configuration that says “expose the `todos` toolset”.
- MCP codegen emits:
  - A server that lists and executes tools using the same provider specs package.

## Implementation Plan (High Level)

This document is complemented by the detailed step‑by‑step plan captured in the workspace plan object. At a high level:

- **A. Semantics & DSL/Expr**:
  - Enforce global toolset name uniqueness.
  - Preserve global tool name uniqueness and ensure per‑toolset uniqueness.
  - Track `OwnerService` and implementation kind as metadata.
- **B. Single Tools Generator**:
  - Emit provider‑anchored specs/types/codecs/transforms at `gen/<service>/tools/<toolset>`.
- **C. Consumers & Runtime**:
  - Refactor agent/MCP codegen to import provider specs, emit typed builders and executors, and align runtime registrations and planner advertising with the tools‑first model.
- **D. Migration & Cleanup**:
  - Migrate fixtures/goldens, remove legacy per‑agent specs paths, and update docs to present the tools‑first model as the default mental model.

