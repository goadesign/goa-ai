# Inline Agent Composition (Cross‑Process)

This document describes the desired end‑state for composing agents inline across
process boundaries, followed by an implementation plan and a progress tracker.

## Outcome

- “Inline” means the nested agent executes as part of the parent workflow’s
  deterministic history, even when the nested agent’s workers run in a different
  process.
- The parent runtime schedules the nested agent’s `Plan`/`Resume` activities on
  the nested agent’s task queue; the engine routes them to remote workers.
- A single stream of `tool_start` / `tool_result` events is emitted by the parent
  runtime for both agent and nested agent tools.
- Canonical JSON flows end‑to‑end (`json.RawMessage` via Goa Meta). Each tool’s
  payload/result is decoded exactly once at the tool boundary using generated codecs.
- No network I/O or planner calls in workflow code; all I/O happens in activities.

## Plan

1. Document inline composition model in runtime & DSL docs.
2. Add `AgentRouteRegistration` and `Runtime.RegisterAgentRoute` for route‑only metadata.
3. Update `ExecuteAgentInline` to orchestrate via activities and to fall back to
   route‑only registration when the planner is not locally registered.
4. Retain `ToolsetRegistration.Inline` semantics and ensure `executeToolCalls` invokes
   inline toolsets directly inside the workflow loop; generated agent‑tool `Execute`
   must call `ExecuteAgentInline` (no network calls in workflow code).
5. Codegen: add exporter template `Register<Agent>Route(ctx, rt)` to register route‑only
   metadata (workflow/queue, plan/resume/execute names + options, policy, specs).
6. Codegen: consumer registry calls `<exporter>.Register<Agent>Route(ctx, rt)` when
   DSL declares use of an exported toolset.
7. Codegen: consumer auto‑registers an inline agent‑tool `ToolsetRegistration` whose
   `Execute` calls `ExecuteAgentInline`.
8. Update event streaming docs and confirm `ToolCallScheduled` / `ToolResultReceived`
   translation to `tool_start` / `tool_result` remains unchanged.
9. Add examples/golden tests for route‑only registration and consumer auto‑wiring.
10. Provide a migration note for replacing ad‑hoc bridges with DSL‑driven auto‑wiring.

## Progress

- [x] 1. Docs updated (runtime & DSL) to describe inline composition and route‑only registration.
- [x] 2. Runtime: `AgentRouteRegistration` type and `RegisterAgentRoute` added.
- [x] 3. Runtime: `ExecuteAgentInline` now orchestrates via activities and supports
      route‑only fallback.
- [x] 4. Runtime: Inline toolset execution path maintained; agent‑tool `Execute` is
      expected to call `ExecuteAgentInline`.
- [x] 5. Codegen: Piggyback provider metadata on toolset registration (no route files).
- [x] 6. Codegen: Consumer auto‑registers inline agent‑tool `ToolsetRegistration`.
- [x] 7. Conventions used as fallback for workflow/activity names and queues.
- [x] 8. Streaming docs verified; added clarifying notes in runtime.md.
- [x] 9. Examples/golden tests (examples doc added).
- [x] 10. Migration note (doc added).
