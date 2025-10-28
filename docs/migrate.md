# AURA → goa-ai Migration Guide

This document provides an exhaustive, step-by-step plan to port AURA to the goa-ai framework while preserving existing functionality and improving design elegance. It emphasizes composition: goa-ai supplies the core agent runtime; AURA provides domain-specific planners, policies, subscribers, and clients. Each step includes validation so you can migrate incrementally and safely.

The plan assumes familiarity with AURA (~/src/aura) and goa-ai (this repo). Follow the repository guidelines in this repo (docs/plan.md, README, and the root style guidance).

Goals:
- Keep proven algorithms (planning loops, gating, tool selection) intact where they don’t benefit from rework.
- Compose AURA-specific behavior via policy engines and hook subscribers instead of forking core runtime.
- Favor copy/move over rewrite, keeping file history and minimizing risk.
- No backwards compatibility required: aim for the most elegant, idiomatic end-state.

Outcomes:
- Keep the service name orchestrator and keep it as a Goa service. It wires the goa-ai runtime with Temporal, Mongo, Pulse, and model adapters, and also exposes standard telemetry endpoints (at minimum: `/livez`, `/healthz`, `/metrics`) like other workers.
- Chat agent and ADA planner ported to `planner.Planner` interfaces; ADA exported as an agent-tool.
- Atlas Data (AD) defined as a toolset, executed via activities out-of-process.
- Stream and session persistence integrated via hook subscribers (Pulse + Mongo stores).
- Approval logic expressed via `policy.Engine` with the same effective behavior.

-------------------------------------------------------------------------------

## 0) Prepare, Read, and Inventory

1. Review AURA architecture
   - Read `~/src/aura/docs/ARCHITECTURE.md` end to end to refresh system boundaries, the durable workflow patterns, and the ADA fan‑out/fan‑in contract.

2. Inventory service designs (systematically)
   - For each service under `~/src/aura/services/*` open the `design/` package and skim the Goa designs to understand surface areas and payload types, especially:
     - `front`, `orchestrator`, `chat-agent`, `atlas-data-agent`, `atlas-data`, `session`, `todos`, `diagnostics-agent`, `remediation-*`.

3. Study inter-service interplay (focus set)
   - Trace a chat turn: front → orchestrator → Temporal → chat-agent activities → inference → ADA loop → AD tools → back to orchestrator → Pulse + session persistence.
   - Note where AURA publishes Pulse `ChatEvent`s and where session service persists `SessionEvent`s.

Validation
- Write down a short list of must-preserve contracts: tool schemas for AD, ADA child‑tracking behavior, chat streaming cadence, session persistence semantics (what’s critical vs. nice-to-have).

-------------------------------------------------------------------------------

## 1) Keep Orchestrator As A Goa Service (Runtime Host)

Objective: Replace the bespoke workflow logic while keeping the orchestrator as a Goa service that starts runs and exposes telemetry endpoints.

Steps
1. Add goa-ai to AURA repo `go.mod` temporary replace for local dev:
   - For local dev: `replace goa.design/goa-ai => ../goa-ai`

2. Orchestrator Goa service (in AURA repo)
   - Keep/define the Goa service `orchestrator` with a minimal HTTP API:
     - Start/Run endpoints for chat/diagnostics (mapping to `rt.StartRun` or `rt.Run`).
     - Telemetry endpoints: `/livez`, `/healthz`, `/metrics` (reuse existing handlers if present).
   - In `services/orchestrator/cmd/orchestrator/main.go` wire the runtime:
     - Builds a Temporal engine adapter (`agents/runtime/engine/temporal`).
     - Creates Mongo-backed memory and run stores (`features/memory/mongo`, `features/run/mongo`).
     - Builds Pulse runtime streams (`features/stream/pulse`).
     - Instantiates `runtime.New(runtime.Options{Engine, MemoryStore, RunStore, Stream, Policy: <placeholder>})`.
     - Registers agents (Chat, ADA) and their activities (Plan/Resume/ExecuteTool) via generated helpers (added in later steps).

3. Worker lifecycle
   - Use the Temporal engine defaults (auto-start workers on first run) or call `Worker()`/`Start()` explicitly if you prefer manual control.

Validation
- Build and run `orchestrator`; it should connect to Temporal, Mongo, and Redis and serve HTTP.
- `GET /livez` and `GET /healthz` return 200. `/metrics` scrapes without errors.

-------------------------------------------------------------------------------

## 2) Define Agents and Toolsets in Goa Designs

Objective: Move AURA Goa designs into goa-ai’s agent DSL and generate registries, codecs, and stubs.

Steps
1. Chat agent design
   - Create (or move) `services/chat/design/agents.go` using goa-ai agents DSL:
     - Define `Agent("chat", ...)` with a `Uses` block importing ADA’s exported toolset (added later) and any other toolsets (e.g., todos).
     - Set `RunPolicy` (caps, time budget) mirroring today’s orchestrator chat caps.

2. ADA (atlas-data-agent) design
   - Create `services/atlas-data-agent/design/agents.go`:
     - Define `Agent("atlas_data_agent", ...)`.
     - In `Exports`, publish ADA’s agent-tools that should be invocable by other agents (e.g., high-level data tasks that plan across AD calls). These become agent-as-tool entries.
     - In `Uses` (optional), include ADA-only internal toolsets (e.g., todos helpers) as needed.

3. AD (atlas-data) toolset design
   - In `services/atlas-data/design/tools.go`, define the deterministic tools ADA and chat rely on (list devices, key events, etc.). These mirror existing AD endpoints; transports remain in AD.

4. Generate code
   - `goa gen ...` to emit per-agent packages: runtime registries, tool specs/types/codecs, and registration helpers.
   - Do not edit generated files. Iterate on `design/*.go` until types match.

Validation
- Build: all generated packages compile. Tool specs for AD match current request/response schemas.
- Sanity: write a tiny unit test that loads generated tool specs and marshals a sample payload to ensure codec compatibility with the existing AD JSON.

5. Schema and description review (mandatory)
- Copy exact tool descriptions from AURA into the DSL:
  - Every `Tool` and every `Field` must have a precise `Description("...")`/4th-argument description string matching production wording.
  - Provide `Example(...)` values and validation (`Minimum/Maximum`, `MinLength/MaxLength`, `Enum`, `Format`) where applicable.
- Preserve exact prompts:
  - System prompts and per-tool prompt snippets must be copied verbatim (no paraphrasing). When using templates, ensure `missingkey=error` and keep content identical to existing strings.

Validation
- Diff descriptions/prompts against the current AURA sources; they must match exactly.
- Regenerate and ensure descriptions appear in generated `tool_spec.go` and types.

-------------------------------------------------------------------------------

## 3) Port Planners (Chat, ADA) to `planner.Planner`

Objective: Move the core planning algorithms without changing their logic, wiring them to goa-ai model and memory interfaces.

Steps
1. Model client
   - Replace calls to AURA’s inference-engine RPC with goa-ai Bedrock/OpenAI clients (`features/model/bedrock` or `features/model/openai`).
   - Build the client once in the orchestrator and `rt.RegisterModel("bedrock", client)` if needed, or inject directly into planner constructors.

2. Chat planner
   - Copy the chat iteration logic from `services/orchestrator/workflows/chat_*.go` and the `chat-agent` service into a new `planner` package (e.g., `services/chat/planner`).
   - Implement `PlanStart` and `PlanResume`:
     - Compose messages/system prompt, tool configuration (allowlist), and stream model output if desired.
     - Return `ToolCalls` (batch) or `FinalResponse` matching current behavior.

3. ADA planner
   - Copy ADA loop logic from `services/atlas-data-agent` (e.g., `resume_state.go`, tool gating/selection, retry hint logic, emit tool dedup) into `services/atlas-data-agent/planner`.
   - Implement `PlanStart`/`PlanResume` to request AD tool calls (no AD RPC inside the planner). Keep the method‑specific allowlists and per‑turn gating intact.
   - When ADA is invoked as a tool (agent-as-tool), rely on `ExecuteAgentInline`; do not spawn separate workflows.

4. Wire planners
   - In orchestrator bootstrap, construct planner instances and pass them in the generated `AgentRegistration` for each agent.

Validation
- Unit tests: port existing planner unit tests (if any) and add table-driven tests for critical prompts/allowlists and retry hint classification.
- Dry run: call `rt.RunAgent("chat", messages...)` with tools disabled and verify a clean `FinalResponse` path.

-------------------------------------------------------------------------------

## 4) Execute Tools via Temporal Activities (AD) and Agent-as-Tool (ADA)

Objective: Route tool execution uniformly through the runtime. Deterministic tools execute as activities; exported ADA methods run inline as agent-tools.

Steps
1. AD toolset implementation (must go through Temporal)
   - Create an AD client wrapper in AURA (reuse existing client code).
   - Implement a toolset registration `ToolsetRegistration{ Name: "atlas.read", TaskQueue: "ad", Execute: func(ctx, call) ... }` that dispatches to AD RPCs and returns `planner.ToolResult`.
   - Register this toolset with the runtime; generated specs ensure JSON payload/result fidelity.
   - Ensure tool execution always happens as Temporal activities:
     - The workflow always schedules `ExecuteToolActivity`.
     - The runtime picks the activity queue from the toolset (`TaskQueue: "ad"`).
     - Run a dedicated worker (e.g., `atlas-data-worker`) that registers the generated `ExecuteToolActivity` and the AD toolset; this worker then performs the AD RPCs. ADA and chat planners never call AD directly.

2. ADA agent-as-tool (inline via ExecuteAgentInline)
   - Use generated `NewAgentToolsetRegistration(rt, cfg)` in ADA’s package to expose exported methods as tools.
   - Provide per-tool message templates or texts for agent-tools so ADA planners receive the correct user intent.
   - When these tools run, the runtime calls `ExecuteAgentInline` and tracks parent/child IDs and expected child totals.

3. Zero conditional workflow code
   - All dispatch happens in toolset.Execute; workflow always schedules `ExecuteToolActivity` for tools. AD calls are handled by the AD worker; ADA exported tools run inline; planners do not make network calls to AD directly.

Validation
- Unit test: invoke AD toolset `Execute` for a few tools with canned payloads; assert result envelopes and retry hint propagation.
- Integration: run a chat turn that triggers at least one AD tool and one ADA agent-tool; assert that child updates (`ToolCallUpdatedEvent`) and final tool results arrive.

-------------------------------------------------------------------------------

## 5) Streaming and Session Persistence

Objective: Stream run events and persist timelines using goa-ai hooks and AURA subscribers.

Steps
1. Streaming (Pulse)
   - Construct `features/stream/pulse.RuntimeStreams` with a Pulse client and pass `Sink()` to runtime options.
   - Register an additional AURA subscriber on `rt.Bus` to publish any AURA-specific envelopes your UI needs (if you decide not to migrate UI yet). Otherwise, update `front` to consume the new stream schema directly.

2. Memory and run stores (Mongo)
   - Configure `features/memory/mongo` and `features/run/mongo` with the same MongoDB.
   - The runtime’s default memory subscriber persists tool calls, tool results, assistant messages, and planner notes.
   - If your UI relies on additional event classes, attach another subscriber that appends richer records into your existing `session` collections or a new collection specialized for agents.

3. Turn and child tracking
   - Confirm that `ToolCallScheduledEvent` and `ToolResultReceivedEvent` now carry `ToolCallID` and `ParentToolCallID`. UIs can compute progress as “completed child results / ExpectedChildrenTotal”.

Validation
- End-to-end: start a chat turn, subscribe to `run/<run-id>` via Pulse subscriber or SSE bridge, and verify ordered events (planner → tool_start → tool_end → final assistant message).
- Persistence: query the Mongo run/memory stores and confirm appended entries match the streamed events.

-------------------------------------------------------------------------------

## 6) Policy and Approvals

Objective: Reexpress approval/override logic using `policy.Engine` so runtime caps and allowlists are applied per turn.

Steps
1. Implement a `policy.Engine` in AURA
   - Port the essential logic from `services/orchestrator/approval` into a single engine that:
     - Reads retry hints and enforces clarifying/auto-retry modes where appropriate.
     - Adjusts `CapsState` (max tool calls, consecutive failures).
     - Optionally consults approval rules to disable/enable tools.

2. Register the engine in runtime options and emit audit labels via `Decision.Labels`/`Metadata`.

Validation
- Unit tests: table-driven scenarios that feed retry hints and requested tools; assert allowlist and cap changes.
- End-to-end: trigger a flow that requires an approval decision and confirm tools are disabled/enabled accordingly; verify `PolicyDecisionEvent` on the bus.

-------------------------------------------------------------------------------

## 7) Front-End Integration

Objective: Update `front` to the new event schema and start/run API.

Steps
1. Orchestrator Start/Run API (keep orchestrator as a Goa service)
   - Keep/adjust the orchestrator HTTP endpoints to call `rt.StartRun` (or `rt.Run`) for the chat agent.
   - Return the `RunID` (and optionally `TurnID`) so the UI can subscribe to `run/<run-id>` streams.
   - Keep telemetry endpoints `/livez`, `/healthz`, `/metrics` in the same service.

2. SSE
   - Implement a small bridge that subscribes to Pulse `run/<run-id>` and streams the events to the browser.
   - Update UI to consume `assistant_reply`, `tool_start`, `tool_end`, and `planner_thought` event types.

Validation
- Browser: start a chat; verify real-time updates and final messages match existing behavior.
- Latency: check no duplicate events and progress bars update as children are discovered and complete.

-------------------------------------------------------------------------------

## 8) Deployment and Operations

Steps
1. Environment
   - Configure Temporal namespace and default task queues.
   - Provide Redis/Pulse connection, Mongo URIs, Bedrock/OpenAI credentials via flags/env.

2. Workers
   - Ensure per-queue worker registration matches your toolset topology (AD queue separate from ADA/planner queues if desired).

3. Observability
   - Enable OTEL interceptors in the Temporal engine adapter (already wired by default).
   - Use hook subscribers for structured logs and metrics.

Validation
- Smoke: run a synthetic scenario (no AD) and verify end-to-end health.
- Load: parallel turns with multiple tools to confirm futures and collection order are stable.

-------------------------------------------------------------------------------

## 9) Migration Mechanics (Copy/Move Guidance)

Prefer moving/renaming files to preserve history. Examples:
- Move ADA prompting and gating logic from `services/atlas-data-agent/*.go` into `services/atlas-data-agent/planner` without algorithm changes.
- Move AD client wrappers into a dedicated `clients/atlasdata` package; reuse exactly where possible.
- Copy system prompts and templates verbatim; only translate scaffolding to goa-ai constructs (messages, templates, tool specs).
- Copy tool descriptions verbatim into the DSL (tool-level and field-level descriptions). Use Examples and validations for schema clarity.
- Delete obsolete orchestrator workflow logic once planners and runtime wiring pass integration tests.

-------------------------------------------------------------------------------

## 10) Verification Checklist (End-State)

- Chat/tool parity
  - ADA dynamically discovers child tools; parent’s ExpectedChildrenTotal updates across iterations.
  - UI reconstructs parent/child tree and progress from stream events.
  - Turn sequencing is monotonic and deterministic.
  - Parallel tool execution across task queues works.

- Policy and caps
  - Max tool calls and consecutive failure caps enforced.
  - Approval logic produces expected allowlists and labels.

- Persistence and replay
  - Pulse event shape is consistent and sufficient for UI.
  - Run/memory stores contain the canonical timeline; large payloads handled.
  - Temporal replay is deterministic.

-------------------------------------------------------------------------------

## Appendix A: Step-by-Step Validation Scripts

- Unit test all packages in AURA after each phase:
  - `go test -race -vet=off ./...`

- Minimal end-to-end chat turn:
  1) Start Redis, Mongo, Temporal.
  2) Run orchestrator binary.
  3) Call `POST /api/start_chat` (or equivalent) to invoke `rt.StartRun`.
  4) Subscribe to Pulse `run/<run-id>`; expect: planner_thought → tool_start → tool_end → assistant_reply.

- Tool execution sanity (no planner):
  - Create a small CLI that calls AD toolset.Execute with a known payload and prints result JSON.

- Policy sanity:
  - Inject a retry hint and assert policy disables a tool next turn; verify `policy_decision` hook event.

-------------------------------------------------------------------------------

## Appendix B: Design Notes and Tradeoffs

- We avoid reproducing AURA’s bespoke orchestrator: the runtime loop, futures, child tracking, turn sequencing, and events are in goa-ai.
- We keep ADA’s selection/gating logic intact; the value lies in stable behavior, not in rephrasing algorithms.
- Streaming is generic; AURA can attach its own adapter subscriber to emit legacy envelopes if needed. With no backwards-compat requirement, prefer migrating `front` to the new schema.
- All domain coupling (knowledge, approvals, todos) lives in AURA feature modules implementing the runtime interfaces.
