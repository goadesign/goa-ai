# Runtime/Temporal Activity Boundary Design

## Goal

Keep the public Goa-AI runtime API engine-agnostic while still supporting fast
worker-failure detection and bounded queue wait when the Temporal engine is in
use.

## Problem

The recent timeout work split activity timing into:

- semantic attempt time (`StartToCloseTimeout`)
- queue wait (`ScheduleToStartTimeout`)
- heartbeat-based liveness (`HeartbeatTimeout`)

That split is correct inside the workflow engine, but exposing those knobs via
`runtime.WithWorker(... WorkerConfig{...})` makes the generic runtime API look
Temporal-shaped. The leak is especially visible because the in-memory engine has
no meaningful interpretation for queue-wait or heartbeat settings.

Goa-AI should keep the author-facing and runtime-facing contract focused on
agent semantics, not workflow backend mechanics.

## Decision

### 1. Keep semantic timing public

The public Goa-AI contract remains:

- `RunPolicy.Timing` in the DSL
- run-time semantic overrides such as `runtime.WithTiming(...)`
- queue placement via `runtime.WithWorker(... WorkerConfig{Queue: ...})`

These are engine-agnostic concepts:

- total run budget
- planner attempt budget
- tool attempt budget
- where the agent runs

### 2. Remove workflow-mechanics tuning from `runtime.WorkerConfig`

`WorkerConfig` should no longer expose per-activity `engine.ActivityOptions`.
Those fields describe engine execution mechanics, not runtime semantics, and
should not be the public tuning surface for applications.

The public runtime API will therefore stop exposing:

- per-activity `ScheduleToStartTimeout`
- per-activity `HeartbeatTimeout`
- engine retry/timeout tuning through `runtime.WithWorker`

### 3. Move liveness and queue-wait tuning into the Temporal adapter

Temporal-specific execution mechanics move to `runtime/agent/engine/temporal`.
The Temporal adapter will own configuration for:

- queue-wait timeout, mapped to Temporal `ScheduleToStartTimeout`
- liveness timeout, mapped to Temporal `HeartbeatTimeout`

Those settings are meaningful only for Temporal, so they belong in
`temporal.Options`, not the generic runtime package.

### 4. Keep the cooperative heartbeat loop runtime-owned

Tool and planner implementations should continue to use Goa-AI’s runtime-owned
heartbeat helper rather than Temporal APIs directly. The runtime heartbeat loop
remains engine-neutral and only activates when the engine injects a non-zero
liveness timeout into the activity context.

That preserves a clean layering:

- runtime code emits cooperative liveness signals
- Temporal decides how those signals affect scheduling and retries

## Resulting Contract

### Public Goa-AI runtime contract

- semantic budgets
- queue placement
- planner/tool helper APIs

### Temporal adapter contract

- queue-wait tuning
- liveness / heartbeat tuning
- Temporal-native mapping details

### In-memory engine contract

The in-memory engine continues to honor semantic attempt budgets where they
make sense and ignores Temporal-only liveness mechanics entirely.

## Consequences

### Benefits

- The runtime API becomes conceptually cleaner.
- The in-memory engine remains a first-class engine rather than a partial
  interpretation of Temporal semantics.
- Applications configure Temporal-specific behavior where they already construct
  the Temporal engine.
- Documentation can cleanly explain the difference between semantic time budgets
  and workflow-mechanics liveness controls.

### Trade-off

Applications that currently tune heartbeat or queue-wait behavior through the
runtime will need to move that configuration to Temporal engine construction.
This is intentional: the abstraction boundary becomes stricter and more honest.

## Implementation Notes

- `runtime.WorkerConfig` becomes queue-only again.
- Runtime tests should assert queue rebasing, not engine activity-option
  overlays.
- The Temporal adapter gains explicit activity-class liveness defaults for hook,
  planner, and tool activities.
- Docs should explain:
  - `Timing.Plan` / `Timing.Tools` are semantic attempt budgets
  - Temporal queue wait and liveness are adapter-level deployment tuning

