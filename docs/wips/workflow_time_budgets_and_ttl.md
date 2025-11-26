### Goal

Bound workflow runtime and activity execution to avoid runaway behavior:
- Enforce a per-run wall-clock budget.
- Reserve a small grace period to finalize with a user-facing message when the budget is exhausted.
- Apply a hard workflow TTL (e.g., 15 minutes) at the engine layer as a last resort.
- Cap all activity timeouts to the remaining run time to prevent stragglers.
- Provide sensible defaults and keep configuration simple and consistent.

### Desired Outcome

- Each run obeys `RunPolicy.TimeBudget`. When the budget is nearly exhausted, the runtime finalizes instead of scheduling more work, leaving enough time (the grace) to produce a final assistant message.
- Activities never outlive the remaining run window: activity StartToClose timeouts are capped to the remaining time.
- A hard workflow TTL exists at the Temporal layer (e.g., 15m) to prevent indefinite workflow histories in pathological cases.
- Defaults are opinionated yet overrideable: short for plan/resume, longer for tool execution.

### Plan (Step-by-step)

1) Add `FinalizerGrace` to `runtime.RunPolicy` (default 10s when unset).  
2) Add per-run override `WithRunFinalizerGrace(time.Duration)`.  
3) Compute two cutoffs in the workflow loop:  
   - Budget deadline: `now + TimeBudget`  
   - Hard deadline: `budget deadline + FinalizerGrace`  
4) In `runLoop`, stop scheduling new activities when remaining time <= `FinalizerGrace`, and immediately finalize.  
5) Cap Plan/Resume activity timeouts to the remaining time to the hard deadline.  
6) Cap ExecuteTool activity timeouts to the remaining time to the hard deadline.  
7) Provide defaults on registration: Plan/Resume 30s; ExecuteTool 2m (only when Timeout is zero).  
8) Introduce `engine.WorkflowStartRequest.RunTimeout` and propagate to the Temporal adapter.  
9) Compute `RunTimeout` at start: `min(15m, TimeBudget + FinalizerGrace + 5s)`; use 15m when no budget.  
10) Avoid scheduling activities when remaining < minimal viable timeout (e.g., <3s); finalize instead.  
11) Keep engine generic; changes are contained to runtime/engine boundary (no type coupling).  
12) Document behavior and defaults; tests verify deadline capping and finalize-on-budget behavior.

### Progress Tracker

- [x] 1. Add `FinalizerGrace` to `RunPolicy`
- [x] 2. Add `WithRunFinalizerGrace(...)`
- [x] 3. Compute budget and hard deadlines in `runLoop`
- [x] 4. Gate scheduling and finalize when remaining <= grace
- [x] 5. Cap Plan/Resume timeouts to remaining time
- [x] 6. Cap ExecuteTool timeouts to remaining time
- [x] 7. Apply default activity timeouts at registration
- [x] 8. Add `RunTimeout` to `WorkflowStartRequest` and use in Temporal
- [x] 9. Compute/start workflows with run TTL (15m cap)
- [x] 10. Minimal viable timeout guard before scheduling
- [x] 11. Docs/comments for rationale and defaults
- [x] 12. Lint and tests green


