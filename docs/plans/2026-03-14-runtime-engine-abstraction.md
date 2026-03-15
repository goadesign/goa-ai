# Runtime Engine Abstraction Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remove Temporal-shaped tuning from the public Goa-AI runtime API while preserving split activity timeout behavior internally and moving queue-wait/liveness tuning to the Temporal adapter.

**Architecture:** Keep semantic timing in the DSL and runtime (`Budget`, `Plan`, `Tools`), reduce `runtime.WorkerConfig` to queue placement, and let the Temporal engine map adapter-owned queue-wait and liveness settings onto Temporal activity options. The runtime-owned cooperative heartbeat loop stays unchanged and activates only when the engine injects a liveness timeout.

**Tech Stack:** Go 1.24, Goa-AI runtime, Temporal SDK, testify

---

### Task 1: Record The Approved Boundary

**Files:**
- Create: `docs/plans/2026-03-14-runtime-engine-abstraction-design.md`
- Create: `docs/plans/2026-03-14-runtime-engine-abstraction.md`

**Step 1: Write the approved design note**

Describe the accepted boundary:
- public runtime API exposes semantic budgets and queue placement only
- Temporal adapter owns queue-wait and liveness tuning
- in-memory stays ignorant of Temporal-only mechanics

**Step 2: Save this implementation plan**

Capture the exact file list, tests, and migration steps before further edits.

**Step 3: Verify both documents exist**

Run: `git status --short`
Expected: both plan files appear as added files.

**Step 4: Commit**

```bash
git add docs/plans/2026-03-14-runtime-engine-abstraction-design.md docs/plans/2026-03-14-runtime-engine-abstraction.md
git commit -m "docs: capture runtime engine boundary plan"
```

### Task 2: Remove Runtime-Level Engine Tuning

**Files:**
- Modify: `runtime/agent/runtime/runtime.go`
- Modify: `runtime/agent/runtime/activity_options.go`
- Modify: `runtime/agent/runtime/runtime_test.go`
- Modify: `runtime/agent/runtime/test_helpers_test.go`

**Step 1: Write the failing test for the new public contract**

Replace the worker override test with one that proves:
- `WithWorker(... WorkerConfig{Queue: ...})` still rebases workflow/planner/tool queues
- no public runtime test depends on `PlanActivityOptions`, `ResumeActivityOptions`, or `ExecuteToolActivityOptions`

**Step 2: Run the runtime test**

Run: `go test ./runtime/agent/runtime -run 'TestRegisterAgentAppliesWorkerQueueOverride'`
Expected: FAIL because the runtime still expects the removed worker activity option fields.

**Step 3: Remove the public worker activity-option fields**

Change `WorkerConfig` so it exposes queue placement only. Delete the merge logic that overlays `engine.ActivityOptions` from `WorkerConfig`, but keep `withDefaultActivityOptions(...)` so runtime-owned default attempt budgets still apply to generated registrations.

**Step 4: Run the runtime test again**

Run: `go test ./runtime/agent/runtime -run 'TestRegisterAgentAppliesWorkerQueueOverride|TestStartRunUsesPolicyBudgetForRunTimeout'`
Expected: PASS.

**Step 5: Commit**

```bash
git add runtime/agent/runtime/runtime.go runtime/agent/runtime/activity_options.go runtime/agent/runtime/runtime_test.go runtime/agent/runtime/test_helpers_test.go
git commit -m "refactor: keep runtime worker config engine-agnostic"
```

### Task 3: Add Temporal-Owned Activity Liveness Tuning

**Files:**
- Modify: `runtime/agent/engine/temporal/engine.go`
- Modify: `runtime/agent/engine/temporal/workflow_context.go`
- Modify: `runtime/agent/engine/temporal/workflow_context_test.go`

**Step 1: Write the failing Temporal test**

Add tests that prove Temporal adapter defaults can provide:
- planner queue-wait timeout
- planner liveness timeout
- tool liveness timeout

without the runtime passing those values through `WorkerConfig`.

**Step 2: Run the Temporal test**

Run: `go test ./runtime/agent/engine/temporal -run 'TestActivityOptionsForAppliesTemporalActivityDefaults|TestActivityOptionsForUsesExplicitTimeoutFields'`
Expected: FAIL because `temporal.Options` does not yet expose adapter-owned liveness defaults.

**Step 3: Add adapter-owned Temporal tuning**

Introduce a Temporal-owned options block for activity-class tuning. Map it at registration time so:
- runtime/public semantics keep supplying `StartToCloseTimeout`
- Temporal-specific queue-wait defaults fill `ScheduleToStartTimeout` when unset
- Temporal-specific liveness defaults fill `HeartbeatTimeout` when unset
- explicit registration overrides still win

**Step 4: Run the Temporal tests again**

Run: `go test ./runtime/agent/engine/temporal -run 'TestActivityOptionsForAppliesTemporalActivityDefaults|TestActivityOptionsForUsesExplicitTimeoutFields|TestActivityOptionsForDefaultsScheduleToStartToStartToClose'`
Expected: PASS.

**Step 5: Commit**

```bash
git add runtime/agent/engine/temporal/engine.go runtime/agent/engine/temporal/workflow_context.go runtime/agent/engine/temporal/workflow_context_test.go
git commit -m "feat: move activity liveness tuning into temporal engine"
```

### Task 4: Update Documentation To Match The Boundary

**Files:**
- Modify: `docs/runtime.md`
- Modify: `docs/dsl.md`
- Modify: `docs/overview.md`
- Modify: `../goa.design/content/en/docs/2-goa-ai/runtime.md`
- Modify: `../goa.design/content/en/docs/2-goa-ai/dsl-reference.md`

**Step 1: Update the runtime docs**

Explain that:
- `Timing.Plan` and `Timing.Tools` are semantic attempt budgets
- `runtime.WithWorker` is for placement, not workflow-engine tuning
- Temporal queue-wait and liveness tuning live in `temporal.Options`

**Step 2: Update the DSL docs**

Clarify that `Timing` does not expose engine internals. It sets semantic planner/tool attempt budgets only.

**Step 3: Update the public goa.design docs**

Mirror the same explanation in the English runtime and DSL reference pages so the external docs and repository docs say the same thing.

**Step 4: Verify the docs read consistently**

Run: `rg "WithWorker|Heartbeat|ScheduleToStart|Timing\\(|Plan\\(|Tools\\(" docs ../goa.design/content/en/docs/2-goa-ai`
Expected: the remaining matches describe the new boundary consistently and do not present `runtime.WorkerConfig` as a Temporal activity tuning surface.

**Step 5: Commit**

```bash
git add docs/runtime.md docs/dsl.md docs/overview.md ../goa.design/content/en/docs/2-goa-ai/runtime.md ../goa.design/content/en/docs/2-goa-ai/dsl-reference.md
git commit -m "docs: explain runtime and temporal timing boundary"
```

### Task 5: Full Verification

**Files:**
- Modify: `runtime/agent/runtime/runtime.go`
- Modify: `runtime/agent/engine/temporal/engine.go`
- Modify: `runtime/agent/engine/temporal/workflow_context.go`
- Modify: `docs/runtime.md`
- Modify: `docs/dsl.md`
- Modify: `docs/overview.md`
- Modify: `../goa.design/content/en/docs/2-goa-ai/runtime.md`
- Modify: `../goa.design/content/en/docs/2-goa-ai/dsl-reference.md`

**Step 1: Format touched Go files**

Run: `gofmt -w runtime/agent/runtime/runtime.go runtime/agent/runtime/activity_options.go runtime/agent/runtime/runtime_test.go runtime/agent/runtime/test_helpers_test.go runtime/agent/engine/temporal/engine.go runtime/agent/engine/temporal/workflow_context.go runtime/agent/engine/temporal/workflow_context_test.go`
Expected: no output.

**Step 2: Run targeted package tests**

Run: `go test ./runtime/agent/runtime ./runtime/agent/engine/... ./codegen/agent`
Expected: PASS.

**Step 3: Read lints for touched paths**

Run the editor lints for:
- `runtime/agent/runtime`
- `runtime/agent/engine/temporal`
- `docs`

Expected: no new diagnostics.

**Step 4: Check the diff**

Run: `git diff -- runtime/agent/runtime runtime/agent/engine/temporal docs ../goa.design/content/en/docs/2-goa-ai`
Expected: runtime surface is cleaner, Temporal owns liveness tuning, docs align.

**Step 5: Commit**

```bash
git add runtime/agent/runtime runtime/agent/engine/temporal docs ../goa.design/content/en/docs/2-goa-ai
git commit -m "refactor: move activity liveness behind temporal engine"
```

