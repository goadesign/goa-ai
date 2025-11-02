# Goa-AI Elegance Refactor Plan

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
    // Get a client for a specific agent
    client, err := rt.Agent(agentID)

    // Use the client to run the agent
    output, err := client.Run(ctx, messages, WithTurnID("..."), WithMetadata(...))
    ```
*   **Functional Run Options**: All operational parameters for a run will be passed via `...RunOption` variadic functions (e.g., `WithMetadata(map[string]any)`, `WithSystemPrompt(string)`), providing a clean and extensible API.
*   **Rich Introspection & Observability**: The runtime will expose a rich, synchronous API for tooling and UI support (`rt.ListAgents()`, `rt.ToolSpec()`) and a simplified helper for streaming events (`rt.SubscribeRun(...)`).
*   **Typed Errors**: All public methods will return exported, typed sentinel errors (e.g., `ErrAgentNotFound`) for robust error handling.

### 2.2. Part 2: Code Generation
The code generator will be overhauled to produce highly ergonomic, type-safe scaffolding that eliminates boilerplate.

*   **`Config/New/Register` Pattern**: For each agent, the generator will produce:
    *   A `Config` struct to declare dependencies (`InferenceClient` for local, `Remote` for remote).
    *   A `New(Config)` factory function that encapsulates the local-vs-remote planner instantiation logic.
    *   A `Register(rt, cfg)` function as the sole, uniform way to register an agent.
*   **Agent-Specific Planner Interface**: The generator will create a named, type-safe interface for each agent's planner (e.g., `chat.Planner`) with methods corresponding to the agent lifecycle (`OnStart`, `OnToolResult`).
*   **Type-Safe IDs**: The generator will produce `const` definitions for all agent and tool identifiers of type `tools.ID`, eliminating stringly-typed references.
*   **Remote Infrastructure**: The generator will create the server-side transport handlers and client-side stubs required for remote planner execution.

### 2.3. Part 3: Advanced Features
*   **Flexible Agent-as-Tool**: The framework will provide a default mechanism for using an agent as a tool (by combining its `SystemPrompt` and the tool call payload), with an optional `WithPromptBuilder` for custom logic.
*   **Runtime Policy Overrides**: A new `rt.OverridePolicy` method will allow for dynamic, in-process tuning of agent execution policies for experiments or operational backoffs.

---

## 3. Detailed Implementation Plan

This section provides a concrete inventory of changes on a per-package basis.

### 3.1. New Packages to Be Added

*   `runtime/agent/engine/inmem/`: The new default, in-memory engine implementation.
*   `runtime/agent/remote/`: Contains the `Remote` interface and `NewPlanner` adapter for transport-agnostic remote planner support.

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
    *   `agent/ids.go.tpl`: Template for the type-safe `tools.ID` constants.
    *   Templates for generating remote planner server handlers and clients.
*   **To Be Modified**:
    *   `agent_tools.go.tpl`: Update to use and generate `tools.ID` constants.

#### **`runtime/agent/runtime/` Package**
*   **To Be Removed**:
    *   `Run()`, `Start()`: All top-level execution helper functions.
    *   `Runtime.RunAgent()`, `Runtime.StartAgent()`, `Runtime.Run()`, `Runtime.StartRun()`: All existing public execution methods on the `Runtime` struct.
    *   `RunInput` struct: Replaced by the `...RunOption` pattern.
    *   `AgentToolConfig` struct: Becomes an internal, un-exported detail.
    *   `NewAgentToolsetRegistration()`: Made obsolete by the new agent-as-tool defaults.
    *   `ValidateAgentToolCoverage()`: Strict validation is removed in favor of flexible defaults.
*   **To Be Added**:
    *   `AgentClient` interface: The new public interface for agent interaction.
    *   `RunOption` type and associated `With...()` functions.
    *   `Runtime.Agent(id tools.ID) (AgentClient, error)`: New method to get an agent client.
    *   `Runtime.SubscribeRun(...)`: High-level helper for streaming.
    *   `Runtime.OverridePolicy(...)`: For runtime policy adjustments.
    *   `Runtime.ListAgents()`, `Runtime.ToolSpec()`, etc.: The new introspection API.
    *   A full suite of exported typed errors (e.g., `ErrAgentNotFound`).
*   **To Be Modified**:
    *   `runtime.New()`: Modify to instantiate the `inmem` engine by default.

#### **`runtime/agent/planner/` Package**
*   **To Be Modified**:
    *   `Planner` interface: Demoted to an internal-only interface.
    *   `ToolRequest` struct: The `Name` field will be changed from `type string` to `type tools.ID`.

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
        *   Re-implement using the new `agent.Register(rt, cfg)` and `rt.Agent(id).Run()` patterns.

5.  **Rewrite Documentation**:
    *   Delete and rewrite the `quickstart` guide and all other user-facing documentation from scratch to reflect the new, simplified, and elegant architecture.
