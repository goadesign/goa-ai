# Goa-AI Elegance Refactor Plan

## Progress

- Phase 1 — Foundation (inmem, AgentClient, RunOptions, typed errors)
  - [x] API added (AgentClient, RunOptions, typed errors)
  - [x] In-memory engine runs quickstart
  - [x] Unit tests for options and errors
  - [x] Docs updated (quickstart, runtime)

- Phase 2 — Codegen Overhaul (Config/New/Register, typed IDs, transforms)
  - [x] Generated packages compile (smoke via tests/templates)
  - [x] Golden tests updated (templates, transforms)
  - [x] Docs updated (DSL/codegen)
  - [x] Per-agent Route() and NewClient(rt) emitted in agent.go
  - [x] Typed tool-call helpers (New<Tool>Call, CallOption) emitted in agenttools
  - [x] Specs packages emit typed tool ID constants and use them in Specs
  - Notes: AgentID constants exposed; WithMCPCaller builder added; transforms unchanged; AgentClient.Run returns *RunOutput.

- Phase 3 — Runtime Refactor (remove old helpers, introspection, SubscribeRun, policy override, per‑agent workers)
  - [x] Old entry points removed; build passes
    - [x] Generated agent Run/Start helpers removed from templates
    - [x] Quickstart/docs switched to AgentClient
    - [x] Remove legacy runtime Run/Start methods (switch callers to AgentClient)
  - [x] Introspection API tests
  - [x] SubscribeRun tested with fake sink
  - [x] Remove workflow/activities stub files; register handlers via closures only
  - [x] Per‑agent worker options and docs (WithWorker, WithQueue)
  - [x] Policy override tests

- Phase 4 — Agent-as-Tool (defaults, PromptBuilder)
  - [x] Default content path tests
  - [x] Prompt builder option tests
  - [x] Docs updated

- Phase 5 — Strong Typed IDs (tools.Ident, agent.Ident)
  - [x] planner.ToolRequest.Name → tools.Ident
  - [x] Runtime/codegen use agent.Ident for agent IDs
  - [x] All packages compile; goldens updated

- Phase 6 — Cleanup + Docs
  - [x] Deprecated APIs removed
  - [x] Quickstart/runtime docs finalized (agent.Ident/tools.Ident reflected)
  - [x] Examples green
  - [x] Docs/examples prefer generated IDs (e.g., chat.AgentID, tool consts)

## 1. Overview

### 1.1. Goal
This document outlines a direct, holistic refactoring of the `goa-ai` framework. The primary goal is to achieve a more elegant, idiomatic, type-safe, and powerful architecture.

### 1.2. Core Principle: No Backward Compatibility
This refactoring is a "rip and replace" effort. The code is not yet in production, freeing us to break existing APIs in pursuit of the ideal design without the burden of incremental migration or deprecation cycles.

### 1.3. Pillars of the New Architecture
*   **DSL-First, Enhanced by Codegen**: Leverage the existing DSL-first approach while making the code generator smarter to eliminate boilerplate and enforce type safety.
*   **Ergonomic and Minimal API**: Expose a small, intuitive API surface that is easy to learn and use.
*   **Zero-Configuration Start**: Enable a "hello, world" agent to run out-of-the-box with zero configuration by using a default in-memory engine.
*   **Unified Local & Remote Execution**: Abstract the location of an agent's planner, allowing the orchestrator to register and interact with local and remote agents uniformly.

---

## 2. The New Architecture by Component

### 2.1. Part 1: Core Runtime and Engine
The runtime will be refactored to be simpler, more powerful, and require zero initial configuration.

*   **Default In-Memory Engine**: A new `engine/inmem` package will provide a fully functional, single-process engine. `runtime.New()` will use this by default, making a Temporal dependency optional for advanced use cases.
*   **Unified `AgentClient` API**: The primary interaction point with an agent will be via an `AgentClient`. All other execution methods on the runtime will be removed.
    ```go
    // Get a client for a specific agent (generated wrapper, no local registration required in callers)
    client := chat.NewClient(rt)

    // Use the client to run the agent
    out, err := client.Run(ctx, messages,
        runtime.WithSessionID("s-123"),
        runtime.WithTurnID("t-1"),
        runtime.WithLabels(map[string]string{"tenant": "acme"}),
        runtime.WithMetadata(map[string]any{"source": "http"}),
        runtime.WithTaskQueue("orchestrator.chat"),
    )
    ```
*   **Functional Run Options (complete list)**: All operational parameters for a run are set via `...RunOption`.
    ```go
    // Identity and grouping
    WithSessionID(id string) RunOption
    WithRunID(id string) RunOption
    WithTurnID(id string) RunOption

    // Caller-provided context
    WithLabels(labels map[string]string) RunOption
    WithMetadata(meta map[string]any) RunOption

    // Engine options (direct)
    WithTaskQueue(name string) RunOption
    WithMemo(m map[string]any) RunOption
    WithSearchAttributes(sa map[string]any) RunOption

    // Escape hatch for advanced callers (applies last)
    WithWorkflowOptions(o *WorkflowOptions) RunOption
    ```
    Notes:
    - `WithSessionID` is required; `Run` returns a typed error when missing.
    - `WithWorkflowOptions` overrides any previously set per-field options.
*   **Rich Introspection & Observability**: The runtime exposes a minimal, synchronous API for UIs/tooling and a helper for typed streaming:
    ```go
    // Discovery
    ListAgents() []agent.Ident
    ListToolsets() []string
    ToolSpec(name tools.Ident) (tools.ToolSpec, bool)
    ToolSpecsForAgent(agentID agent.Ident) []tools.ToolSpec
    ToolSchema(name tools.Ident) (map[string]any, bool) // parsed JSON Schema

    // Streaming (per-run)
    SubscribeRun(ctx context.Context, runID string, sink stream.Sink) (close func(), err error)
    ```
    SubscribeRun installs a filtered subscriber for the given run ID and returns a closer; events are ordered per run and typed.
*   **Typed Errors (catalog)**: All public methods return exported sentinels for robust handling.
    ```go
    var (
        ErrAgentNotFound        = errors.New("agent not found")
        ErrEngineNotConfigured  = errors.New("runtime engine not configured")
        ErrInvalidConfig        = errors.New("invalid configuration")
        ErrMissingSessionID     = errors.New("session id is required")
        ErrWorkflowStartFailed  = errors.New("workflow start failed")
    )
    ```

### 2.2. Part 2: Code Generation
The code generator will be overhauled to produce highly ergonomic, type-safe scaffolding that eliminates boilerplate.

*   **`Config/New/Register` Pattern**: For each agent, the generator produces:
    *   `Config` for dependencies (local planner, remote client, MCP callers if used).
    *   `New(Config)` factory that instantiates the package and validates config.
    *   `Register(rt, cfg)` as the single way to register with the runtime.
    *   Convenience builders where applicable, e.g. `WithMCPCaller(id string, caller mcpruntime.Caller)` to avoid map wiring in user code.
*   **Agent-Specific Planner Interface**: A named, type-safe interface per agent (e.g., `chat.Planner`) with lifecycle methods (`OnStart`, `OnToolResult`) mapped to plan/resume.
*   **Type-Safe IDs**: `const` definitions for agent IDs (`tools.AgentID`) and tool IDs (`tools.ToolID`) used everywhere in generated code and runtime calls.
*   **Transforms Continuity**: When tool Arg/Return match method Payload/Result, emit per-tool transforms under `specs/<toolset>/transforms.go`:
    ```go
    ToMethodPayload_<Tool>(in <ToolArgs>) (<MethodPayload>, error)
    ToToolReturn_<Tool>(in <MethodResult>) (<ToolReturn>, error)
    ```
    Executors continue to use these helpers to avoid boilerplate mappings.
*   **Per‑Agent Worker Helper**: Generate a small helper in each agent package to construct a worker config for that agent only:
    ```go
    // In gen/<svc>/agents/<agent>/agent.go
    func NewWorker(opts ...runtime.WorkerOption) runtime.WorkerConfig
    ```
    Options (e.g., `WithQueue`) live in the runtime package, not generated code.

### 2.3. Part 3: Advanced Features
*   **Flexible Agent-as-Tool**: Default content is constructed when no per-tool text/template is provided: `SystemPrompt` (if set) followed by a user message from the payload (`PayloadToString`). Callers can customize message construction with a builder option:
    ```go
    type PromptBuilder func(id tools.Ident, payload any) string
    WithPromptBuilder(b PromptBuilder) AgentToolOption
    ```
    Template compilers/validators remain available for strict configurations; defaults favor flexibility.
*   **Runtime Policy Overrides**: `rt.OverridePolicy(id agent.Ident, delta RunPolicy) error` allows in-process tuning of caps/time budgets/interrupt flags for experiments or operational backoffs. Overrides affect new runs and are local to the process.

---

## 2.4. API Sketches (Reference)

These sketches are normative for implementation.

Note on typed IDs: use `tools.Ident` for tools and `agent.Ident` for agents
consistently across runtime, hooks, planner, and codegen. The runtime does not
re-export ID aliases.

```go
// Agent client
type AgentClient interface {
    Run(ctx context.Context, messages []planner.AgentMessage, opts ...RunOption) (*RunOutput, error)
    Start(ctx context.Context, messages []planner.AgentMessage, opts ...RunOption) (engine.WorkflowHandle, error)
}

// Runtime entry points
func (r *Runtime) Client(id agent.Ident) (AgentClient, error)
func (r *Runtime) MustClient(id agent.Ident) AgentClient
// Route-based client for caller-only processes
type AgentRoute struct { ID agent.Ident; WorkflowName, DefaultTaskQueue string }
func (r *Runtime) ClientFor(route AgentRoute) (AgentClient, error)
func (r *Runtime) MustClientFor(route AgentRoute) AgentClient

// Run options (see complete list above)
type RunOption func(*RunInput)

// Introspection
func (r *Runtime) ListAgents() []agent.Ident
func (r *Runtime) ListToolsets() []string
func (r *Runtime) ToolSpec(name tools.Ident) (tools.ToolSpec, bool)
func (r *Runtime) ToolSpecsForAgent(agentID agent.Ident) []tools.ToolSpec
func (r *Runtime) ToolSchema(name tools.Ident) (map[string]any, bool)

// Streaming helper
func (r *Runtime) SubscribeRun(ctx context.Context, runID string, sink stream.Sink) (func(), error)

// Workers (per agent)
type WorkerConfig struct { Queue string }
type WorkerOption func(*WorkerConfig)

// Runtime options for workers
func WithWorker(id agent.Ident, cfg WorkerConfig) RuntimeOption
func WithQueue(name string) WorkerOption

// Invariants
// - Workers are supplied at runtime construction; late attach is not supported.
// - Agents should be registered before first run; registration may be closed after first run.

// Errors
var (
    ErrAgentNotFound          error
    ErrEngineNotConfigured    error
    ErrInvalidConfig          error
    ErrMissingSessionID       error
    ErrWorkflowStartFailed    error
)

---

## 2.5. Ergonomics via Codegen (Keep Runtime Minimal)

We keep the runtime API lean and push ergonomics into generated packages. This
lets most users learn a tiny surface while still giving advanced users power
hooks. The guiding rule: generated code adds convenience; runtime adds
foundational primitives only.

### 2.5.1. Per‑Agent Typed Client (Generated)

Generate a typed constructor per agent so application code need not pass IDs
or discover clients dynamically:

```go
// In gen/<svc>/agents/<agent>/agent.go
// Route provides the remote workflow route for caller-only processes.
func Route() runtime.AgentRoute { return runtime.AgentRoute{ID: AgentID, WorkflowName: "<generated>", DefaultTaskQueue: "<generated>"} }

// NewClient uses the route so callers don’t need to register agents locally.
func NewClient(rt *runtime.Runtime) runtime.AgentClient { return rt.MustClientFor(Route()) }
```

Docs/examples will use `<agent>.NewClient(rt)` rather than `runtime.Client` in
normal paths. We keep `Runtime.Client/MustClient` as advanced hooks for dynamic
selection (rare but important) but do not promote them in basic docs.

### 2.5.2. Typed Tool‑Call Helpers (Generated)

For each exported tool, generate a helper that constructs a
`planner.ToolRequest` with the typed payload and generated tool identifier:

```go
// In gen/<svc>/agents/<agent>/agenttools/<toolset>/helpers.go
// NewSearchCall builds a tool request with the proper tool identifier.
func NewSearchCall(args SearchArgs, opts ...CallOption) planner.ToolRequest {
    // Set Name to the generated constant, marshal args if needed, apply meta options
}
```

This reduces ad‑hoc `planner.ToolRequest` assembly and nudges users to
consistently use generated identifiers instead of ad‑hoc casts.

### 2.5.3. Keep Advanced Hooks in Runtime (Documented)

The following remain exported and documented as “Advanced & Generated Integration”:

- `ExecuteWorkflow`
- `PlanStartActivity`
- `PlanResumeActivity`
- `ExecuteToolActivity`

Normal applications should prefer the typed, high‑level client and tool helpers.

### 2.5.4. No Runtime API Growth for Ergonomics

We do not add new runtime factories for `AgentClient` or tool helpers. All
ergonomics live in generated code. The runtime surface remains:

- Construction: `runtime.New(...)` (+ options)
- Execution:
  - Local registration: `Runtime.Client(...).Run/Start` (advanced, dynamic across multiple agents)
  - Remote route: `Runtime.ClientFor(route).Run/Start` (advanced, custom routes)
  - Typical path uses `<agent>.NewClient(rt)` from generated code
- Introspection: `ListAgents`, `ListToolsets`, `ToolSpec`, `ToolSchema`, `ToolSpecsForAgent`
- Streaming: `SubscribeRun`
- Overrides: `OverridePolicy`
- Registration: `RegisterAgent`, `RegisterToolset`, `RegisterModel`

### 2.5.5. Docs & Examples

- Prefer `<agent>.NewClient(rt)` and generated tool constants in docs.
- Include a small "Dynamic & Advanced" section showing `Runtime.Client` and
  `ExecuteWorkflow` usage for power users.

```

---

## 3. Detailed Implementation Plan

This section provides a concrete inventory of changes on a per-package basis.

### 3.1. New Packages to Be Added

*   `runtime/agent/engine/inmem/`: The new default, in-memory engine implementation.

### 3.2. Detailed Deprecation and Modification

#### **`dsl/` Package**
*   **To Be Modified**:
    *   `Agent()`: Update to accept a new `SystemPrompt(string)` child DSL function.

#### **`codegen/agent/templates/` Package**
*   **To Be Removed**:
    *   `agent.go.tpl`: The current template generating top-level `Run`/`Start` functions.
*   **To Be Added**:
    *   `agent/config.go.tpl`: Template for the agent's `Config` struct.
    *   `agent/planner.go.tpl`: Template for the agent-specific `Planner` interface.
    *   `agent/agent.go.tpl`: Template for the `New` and `Register` functions.
    *   `agent/ids.go.tpl`: Template for the type-safe `tools.Ident` constants.
    *   `agent/worker.go` (inlined in agent.go): `NewWorker(opts ...runtime.WorkerOption) runtime.WorkerConfig` helper.
*   **To Be Modified**:
    *   `agent_tools.go.tpl`: Update to use and generate `tools.Ident` constants.

#### **`runtime/agent/runtime/` Package**
*   **To Be Removed**:
    *   `Run()`, `Start()`: All top-level execution helper functions.
    *   `Runtime.RunAgent()`, `Runtime.StartAgent()`, `Runtime.Run()`, `Runtime.StartRun()`: All existing public execution methods on the `Runtime` struct.
    *   `RunInput` struct: Replaced by the `...RunOption` pattern.
    *   `AgentToolConfig` struct: Becomes an internal, un-exported detail.
    *   `NewAgentToolsetRegistration()`: Made obsolete by the new agent-as-tool defaults.
    *   `ValidateAgentToolCoverage()`: Strict validation is removed in favor of flexible defaults.
*   **To Be Added**:
    *   `AgentClient` interface: New public entry point for agent interaction.
    *   `RunOption` + first-class `With...` functions (see Section 2.1).
    *   `Runtime.Client(id AgentID) (AgentClient, error)` and `MustClient(id AgentID)`.
    *   `Runtime.SubscribeRun(ctx, runID, sink)` streaming helper.
    *   `Runtime.OverridePolicy(agentID, delta)` runtime policy adjustments.
    *   Full introspection surface (`ListAgents`, `ToolSpec`, `ToolSpecsForAgent`, `ToolSchema`, `ListToolsets`).
    *   Exported typed errors (see Section 2.1).
    *   Per‑agent worker options: `WithWorker(AgentID, WorkerConfig)`, `WithQueue(...)`.
*   **To Be Modified**:
    *   `runtime.New()`: Modify to instantiate the `inmem` engine by default.

#### **`runtime/agent/planner/` Package**
*   **To Be Modified**:
    *   `Planner` interface: Demoted to an internal-only interface.
    *   `ToolRequest` struct: The `Name` field changes from `string` to `tools.Ident`.

#### **`runtime/agent/hooks/` and `runtime/agent/stream/` Packages**
*   **To Be Kept (for internal/advanced use)**: These packages provide the low-level plumbing for the new `SubscribeRun` helper and remain available for advanced observability use cases.

---

## 4. Execution Strategy

This is a direct, "rip and replace" refactoring. The following sequence is recommended:

1.  **Lay the Foundation**:
    *   Implement the new packages: `engine/inmem` and `runtime/agent/remote`.
    *   Define the new public interfaces in the runtime: `AgentClient`, `RunOption`, and the new introspection/observability methods.
    *   Introduce the new typed errors.

2.  **Overhaul the Code Generator**:
    *   Implement all the "To Be Added" and "To Be Modified" changes for the `codegen` package. This is the most critical step. The new generator should produce the `Config/New/Register` pattern, agent-specific planners, and type-safe IDs.

3.  **Refactor the Runtime**:
    *   Delete all the "To Be Removed" constructs from the `runtime` package.
    *   Implement the logic for the new public APIs (`rt.Agent`, etc.), wiring them to the internal components.
    *   Update `runtime.New()` to use the `inmem` engine by default.

4.  **Refactor Applications**:
    *   For each agent in the project:
        *   Delete the old planner implementation.
        *   Re-implement it using the new generated agent-specific `Planner` interface.
    *   In the orchestrator `main` function:
        *   Remove all old agent registration and execution logic.
        *   Re-implement using the new `agent.Register(rt, cfg)` and `<agent>.NewClient(rt).Run()` patterns.

5.  **Rewrite Documentation**:
    *   Delete and rewrite the `quickstart` guide and all other user-facing documentation from scratch to reflect the new, simplified, and elegant architecture.
