## goa-ai Test Plan

### Purpose

Define a pragmatic, high-signal testing strategy to gain confidence that goa-ai’s agent runtime, codegen glue, and feature modules work as intended. This plan addresses current gaps while keeping tests fast, deterministic, and maintainable.

### Confidence Goals

- Validate the durable run loop (Plan → Execute tools → Resume → Final) with policy/caps, interrupts, and event emission.
- Ensure tool execution is correct across types (service-backed, agent-as-tool) with proper codec use, error/RetryHint propagation, queues, and parallelism.
- Verify Temporal adapter injects workflow context into activities and forwards options.
- Keep MCP integration confidence via stable scenario tests.
- Avoid flakes; prefer deterministic harness-based end-to-end tests over live Temporal for PRs.

## Guiding Principles

- Fast unit tests first: deterministic, no network, cover core logic and contracts.
- Harness mini-e2e: run example workflow entirely in-process to check end-to-end behavior without Temporal or external services.
- Thin adapter conformance: unit-test adapter seams (context injection, options mapping) without standing up Temporal in CI.
- Scenario tests for MCP: keep yaml-driven MCP tests as black-box validation; extend lightly for critical cases.
- Golden-lite: snapshot only essential event fields; ignore timestamps/IDs to prevent flakiness.

## Current Coverage (at a glance)

- Unit tests exist for planner activities, ExecuteToolActivity codec/error paths, policy decision event publishing, child tracker updates, pause/resume signaling, hooks bus/stream subscribers, Mongo/Pulse clients, MCP callers, and a simple harness run.
- Integration tests cover MCP protocol, tools, resources, prompts, notifications via integration_tests/scenarios/*.yaml.

## Highest-ROI Gaps To Close

- Policy across turns: allowlist trimming, caps decrement/reset, consecutive-failure breaker.
- Interrupts in loop: pause mid-turn, resume with extra messages, status transitions, events.
- Agent-as-tool: inline nested execution, child discovery updates, result/telemetry aggregation.
- Parallelism & ordering: async scheduling, ordered collection, deterministic event sequencing.
- Task queues: per-toolset TaskQueue override honored in activity requests.
- Workflow options: forward memo/search attributes/task queue/retry policy to engine.
- Time budget: deadlines enforced with clear failure and events.
- Streaming & memory fidelity: verify tool call/result and assistant events persist/stream correctly.
- Adapter conformance: Temporal engine injects workflow context and applies options/instrumentation.

## Additions By Layer

### 1) Runtime Unit Tests (fast, deterministic)

Add or extend tests under agents/runtime/runtime/:

- Policy enforcement across turns
  - Set stub policy decisions; assert allowlist trimming changes which tools run; ensure CapsState decrements and consecutive-failure breaker trips; labels/metadata persisted in run store.
  - Target: agents/runtime/runtime/runtime_test.go.

- Interrupt pause/resume flow
  - Use test workflow context’s SignalChannel to issue pause/resume; assert RunPausedEvent/RunResumedEvent, run store status transitions, and that resume messages are appended to planner input.
  - Target: agents/runtime/runtime/runtime_test.go.

- Toolset TaskQueue override
  - Register a toolset with TaskQueue; assert ExecuteActivityAsync receives ActivityRequest.Queue set from toolset.
  - Target: agents/runtime/runtime/runtime_test.go.

- WorkflowOptions forwarding
  - Call StartRun with WorkflowOptions (memo/search attributes/task queue/retry policy); assert stub engine captured those values.
  - Target: agents/runtime/runtime/runtime_test.go.

- Time budget enforcement
  - Provide a RunPolicy with tiny TimeBudget, or pass an already-expired deadline into runLoop; assert time-budget exceeded error and completion event semantics.
  - Target: agents/runtime/runtime/runtime_test.go.

- Event stamping
  - With a fixed TurnID, assert ToolCallScheduled, ToolResultReceived, PolicyDecision, AssistantMessage all carry monotonic SeqInTurn for ordering.
  - Target: agents/runtime/runtime/runtime_test.go.

- ActivityToolExecutor path
  - Unit-test ActivityToolExecutor.Execute: provide a fake WorkflowContext that returns a crafted ToolOutput; assert correct pass-through semantics.
  - Target: agents/runtime/runtime/types_test.go (or in runtime_test.go).

- ConvertRunOutputToToolResult
  - Verify telemetry aggregation and “ALL nested tools failed” error propagation; ensure success case returns Final.Content as payload.
  - Target: agents/runtime/runtime/runtime_test.go.

### 2) Harness Mini-E2E (deterministic, no network)

Extend example/complete/runtime_example_test.go (or add example/complete/runtime_harness_e2e_test.go):

- Nested agent-as-tool: have a parent planner emit an agent-tool; nested agent discovers 2 tools then 1 more; assert ToolCallUpdatedEvent fires with totals 2 then 3; assert parent receives aggregated results and error semantics.
- Parallel scheduling, ordered collection: simulate tools with different durations in the example engine; assert results are collected in call order; verify events exist for each tool with deterministic SeqInTurn.
- Memory & stream fidelity: after a run, load memory events and captured stream events; assert presence and shapes for tool call/result and final assistant entries.

### 3) MCP Scenario Extensions (black-box)

Add or extend scenarios under integration_tests/scenarios/:

- Retry hint surfacing
  - Invalid arguments should produce a structured retry hint in the stream; add a scenario that asserts presence of an error event consistent with invalid arguments (no brittle full-payload comparisons).

- Streaming shapes
  - For at least one tool, assert initial response event followed by minimal content assertions (avoid strict ordering beyond what the contract guarantees).

Keep scenarios minimal and stable. Use TEST_FILTER, TEST_DEBUG, and TEST_SERVER_URL as documented in integration_tests/README.md.

### 4) Temporal Adapter Conformance (unit-level seam)

Create targeted tests under agents/runtime/engine/temporal/:

- Context injection to activities
  - Invoke the registered activity closure via a test seam; mock lookupWorkflowContext to return a WorkflowContext and assert engine.WithWorkflowContext is applied.

- Options/instrumentation mapping
  - Validate client/worker instrumentation interceptors are wired when enabled; verify activity options map to internal storage used by workers.

- StartWorkflow queue resolution
  - Ensure StartWorkflow picks workflow/task queue precedence (request, definition, default).

Gate any live Temporal tests behind a build tag for opt-in only (not in PR CI).

## Golden-lite Snapshots (Optional, Stable)

- Capture event streams as JSON Lines (event type + essential fields only: tool name, expected children, message, error flag). Mask dynamic fields (timestamps, IDs, durations). Store in example/complete/testdata/*.golden.jsonl.
- Provide a small helper to serialize runtime hook events into this reduced shape for comparison.
- Always ignore generated header lines in golden comparisons per repo guidelines.

## Where Tests Live

- Runtime unit: agents/runtime/runtime/runtime_test.go, agents/runtime/runtime/types_test.go.
- Engine adapter: agents/runtime/engine/temporal/engine_test.go.
- Harness e2e: example/complete/runtime_example_test.go or example/complete/runtime_harness_e2e_test.go.
- MCP scenarios: integration_tests/scenarios/*.yaml.

## Running Tests

- Lint
  - make lint

- Unit tests (race, fast):
  - make test

- Harness e2e (included in make test):
  - go test ./example -run Harness -race

- MCP scenarios:
  - make itest
  - Filter: TEST_FILTER="tools_list" go test -v ./integration_tests/tests
  - External server: TEST_SERVER_URL=http://localhost:8080 TEST_SKIP_GENERATION=true go test -v ./integration_tests/tests

## CI Strategy

- PR CI
  - make lint
  - make test (includes runtime unit + harness e2e)

- Nightly/optional
  - make itest for MCP scenarios
  - Optional adapter conformance suite (behind a tag) if desired

## Test Authoring Conventions

- Style
  - Use standard testing + testify/require for assertions; table-driven tests where appropriate.
  - Keep unit tests hermetic—no real network or Temporal required.
  - Avoid strict equality on dynamic fields; assert minimal, meaningful invariants.

- Contracts & Guards
  - Validate only at system boundaries per repo guidelines; do not add defensive nil checks in unit-under-test unless contracts require them.

- Golden files
  - Store in example/complete/testdata; ignore generated headers; mask dynamic fields.

## Implementation Checklist (ordered)

- Runtime unit
  - [x] Policy across turns (allowlist, caps, consecutive failures)
  - [x] Interrupt pause/resume in runLoop
  - [x] Toolset TaskQueue honored
  - [x] WorkflowOptions forwarded from StartRun
  - [x] Time budget exceeded path
  - [x] Event stamping (TurnID, SeqInTurn)
  - [x] ActivityToolExecutor path
  - [x] ConvertRunOutputToToolResult aggregation/error

- Harness e2e
  - [ ] Nested agent-as-tool with dynamic child updates
  - [ ] Parallel scheduling with stable collection order
  - [ ] Memory + stream event fidelity assertions

- MCP scenarios
  - [ ] Add invalid-args scenario asserting structured error/RetryHint in stream (minimal assertions)

- Adapter conformance
  - [ ] Activity context injection seam test
  - [ ] Instrumentation/option mapping tests
  - [ ] Queue precedence in StartWorkflow

## Milestones

- Milestone 1 (Runtime confidence): All runtime unit tests green (policy, interrupts, queues, options, budget, stamping, executor helper, conversion).
- Milestone 2 (Harness e2e confidence): Nested agent-as-tool and parallel scheduling tests green with golden-lite snapshots passing.
- Milestone 3 (Adapter/MCP confidence): Temporal adapter conformance tests added; 1–2 new MCP scenarios added; nightly job green.

---

With this plan, PR CI remains fast and deterministic while raising confidence on core behaviors. Nightly runs extend coverage to scenario and adapter paths without burdening contributors.
