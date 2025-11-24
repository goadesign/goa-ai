## goa-ai Agent Streaming & Agent-as-Tool Redesign – Implementation Plan

This document describes a concrete plan to evolve `goa-ai`’s agent
runtime and streaming model to:

- Treat each agent as a first-class actor with an explicit run
  lifecycle.
- Replace flattened, binary visibility (`SuppressChildEvents`) with
  composable stream federation (run links + profiles).
- Give applications (like AURA) a clean, powerful API for agent-as-tool
  composition and UX.

We assume:

- No external consumers: we are free to **break and simplify** APIs.
- High bar for elegance and symmetry; we optimize for a clean end-state.

The plan is organized into implementation phases. Phases are ordered but
can be interleaved where sensible.

---

## Phase 0 – Baseline and constraints

**Goals**

- Clarify invariants of the current runtime:
  - how runs are identified and linked,
  - how hooks and stream subscribers operate,
  - how agent-as-tool is wired (AgentToolConfig, Finalizer).
- Decide global ownership rules:
  - **Single owner for provider streaming**: only the runtime consumes
    `Streamer.Recv`; planners operate on higher-level APIs.
  - **Scoped emission**: events are emitted only in the current run’s
    scope; parent/child projections are explicit.

**Deliverables**

- Short internal note in `docs/wips/` (or comments) capturing:
  - “Runtime owns `Streamer` consumption.”
  - “Decorated clients never emit into parent runs.”
  - “Adapters enforce canonical tool IDs at the edge.”

These decisions guide all later phases.

---

## Phase 1 – Core types: runs, phases, and links

### 1.1 Run identity and handle

Introduce a **run handle** type in `runtime/agent/runtime`:

- `RunID` (string)
- `AgentID` (string, e.g. `service.agent`)
- `ParentRunID` (string, optional)
- `ParentToolCallID` (string, optional)

Add helpers to construct and query handles, and carry them through:

- Workflow context and run context structures.
- `RunOutput` and `RunCompletedEvent`.

### 1.2 Run phases

Define a `RunPhase` enum, for example:

- `RunPhasePrompted`
- `RunPhasePlanning`
- `RunPhaseExecutingTools`
- `RunPhaseSynthesizing`
- `RunPhaseCompleted`
- `RunPhaseFailed`
- `RunPhaseCanceled`

Wire this into:

- The workflow loop in `runtime.go`:
  - Set `Prompted` at start.
  - Set `Planning` during planner execution.
  - Set `ExecutingTools` while tools/child agents run.
  - Set `Synthesizing` if the planner performs a final synthesis pass.
  - Set `Completed`/`Failed`/`Canceled` before emitting `RunCompletedEvent`.
- New or extended hook events:
  - `RunPhaseChangedEvent` (or embed phase in existing run events).

### 1.3 Run links

Define a minimal **run link** abstraction:

- Struct including:
  - `RunID`
  - `AgentID`
  - optional `StreamKey` (transport-specific, e.g. `aura.run.<RunID>`).
- Used as:
  - part of `RunOutput` for nested agent runs,
  - payload for “agent-run-started” events on parent streams.

No behavior change yet; this phase is about types and plumbing.

---

## Phase 2 – Typed event schema and provenance

### 2.1 Hooks and stream event kinds

Extend `runtime/agent/hooks` and `runtime/agent/stream` to support
**typed event kinds** with clear provenance:

- Thinking:
  - `ThinkingStart`
  - `ThinkingChunk`
  - `ThinkingEnd`
- Tools:
  - `ToolRequested` (planner intent to call a tool, optional)
  - `ToolScheduled` (activity or inline scheduling)
  - `ToolResult` (completion with result/telemetry/error)
  - `ToolRetry` (optional, for explicit retry flows)
- Summaries / finalization:
  - `SummaryStart`
  - `SummaryChunk`
  - `SummaryFinal`
- Runs:
  - `RunPhaseChanged`
  - `RunCompleted`

All events include:

- `RunID`, `AgentID`
- `ToolID` (when relevant, canonical `tools.Ident`)
- `ParentRunID`, `ParentToolCallID` (for context)

### 2.2 Stream subscriber projection

Update `stream.Subscriber` to:

- Translate hook events into the new, typed stream events.
- Maintain existing semantics where helpful, but **do not** flatten
child runs; that will be handled by profiles and run links.

At the end of this phase, we have:

- A richer internal event vocabulary,
- Provenance on all events,
- A single place (subscriber) that maps hooks → stream events.

---

## Phase 3 – Canonical tool identity enforcement

### 3.1 Tools.Ident as canonical ID

Clarify and enforce that `tools.Ident`:

- Always uses canonical dot-separated form (`service.toolset.tool`).
- Is the only form used in:
  - `ToolSpec.Name`,
  - hook and stream events (`ToolName`),
  - planner `ToolRequest` and `ToolResult`.

### 3.2 Adapter responsibilities

Update provider adapters (e.g., Bedrock) to:

- Map from canonical IDs ↔ provider-specific IDs at the edge.
- **Reject** invalid or non-canonical IDs early.
- Remove any planner- or service-side guards against provider ID
formats.

This keeps planners and agents provider-agnostic.

---

## Phase 4 – Stream profiles and child stream policies

### 4.1 Stream profiles

Introduce a `StreamProfile` concept in `runtime/agent/stream` (or a
nearby package), describing what an audience sees:

- Which event kinds are included.
- For each kind, whether payloads are:
  - omitted,
  - redacted,
  - fully included.
- Whether child runs are:
  - invisible,
  - flattened,
  - linked only.

Provide helpers to build common profiles:

- `UserChatProfile`
- `AgentDebugProfile`
- `MetricsProfile`

## Phase 5 – Agent-as-tool contract and streaming ownership

### 5.1 Single owner for provider streams

Refactor the streaming usage so that:

- The **runtime** is the only layer that consumes `model.Streamer`
  (calls `Recv` / `Close`).
- Planners use a high-level API that:
  - accepts `model.Request` (with tools, messages, etc.), and
  - receives callbacks or a synthesized `RunOutput` built by the
    runtime.

Concretely:

- Introduce a runtime method like `ExecuteModelStream(ctx, req, sink)`
  or equivalent, used by planners.
- Remove direct `mc.Stream` + manual `Recv` loops from planners.

This avoids double-emit and clarifies ownership of thinking/messages.

### 5.2 Agent-as-tool return shape

Redesign agent-as-tool so that conceptually it returns:

- `ToolResult` (payload + telemetry + errors), plus
- a `RunLink` referencing the nested agent’s run and stream.

Implementation options:

- Embed a `RunLink` field into `planner.ToolResult` (used only for
agent tools).
- Extend agent-tool-specific runtime helpers to construct and carry
`RunLink` alongside `ToolResult`.

Update:

- `AgentToolConfig`, `NewAgentToolsetRegistration`.
- Finalizer logic so that:
  - Finalizers receive `FinalizerInput` with child run/context info.
  - Finalizers do not need to re-stream anything; they only aggregate
    data and may attach `RunLink`.

### 5.3 Execution vs observability symmetry

Ensure symmetry:

- Any time an agent is invoked as a tool:
  - a child run is created,
  - a `RunLink` is available to the parent,
  - a predictable `AgentRunStarted` event is emitted in the parent run
    (subject to profile).

This makes agent-as-tool indistinguishable from “normal” runs from the
runtime’s perspective; only policy and profiles change what is visible.

---

## Phase 6 – Policy, caps, and interrupts alignment

### 6.1 Caps and policy

Audit:

- Caps (`MaxToolCalls`, `MaxConsecutiveFailedToolCalls`).
- Policy modules (tool allow/deny).

Ensure:

- Caps operate on the run tree with clear semantics:
  - root vs child runs,
  - aggregated tool counts vs per-run budgets.
- Policy decisions can be surfaced via structured events (and
optionally visible in debug profiles).

### 6.2 Interrupts and awaits

Align:

- `AwaitClarification` and `AwaitExternalTools` with run phases and
stream profiles:
  - emits appropriate events when a run pauses for human/external
    input,
  - keeps these events scoped to the run that owns the await.

Ensure agent-as-tool can:

- Optionally expose awaits up to the parent via profiles (e.g., linked
events),
- Without forcing parents to handle low-level details unless they opt
in.

---

## Phase 7 – Tests, examples, and documentation

### 7.1 Invariants and tests

Define invariants:

- Every run has exactly one terminal `RunCompleted` event whose status
matches its final `RunPhase`.
- Every `AgentRunStarted` / run link in a parent corresponds to an
existing child run in the run tree.
- Events for a given `RunID` never claim a different `AgentID`.

Add tests:

- Unit tests in `runtime/agent/runtime` and `runtime/agent/stream` for:
  - run phase transitions,
  - event provenance,
  - stream profile filtering.
- Golden tests where appropriate (similar to existing ones) to lock in
wire formats.

### 7.2 Example and docs

Update:

- The example assistant in `codegen` / `example` to:
  - use run links,
  - demonstrate child agents,
  - show how profiles shape streams.
- Documentation:
  - Add or extend docs under `docs/` (runtime, streaming) to:
    - explain runs, phases, run links,
    - describe stream profiles and child policies,
    - show agent-as-tool patterns with `(ToolResult, RunLink)`.

At this point, `goa-ai` has a coherent, symmetric model for agents,
runs, and streams that can be cleanly consumed by systems like AURA.


