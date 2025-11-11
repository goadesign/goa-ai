## Goal: Simple, predictable timeouts with guaranteed finalization

We want a world‑class, intuitive timeout model that:
- Makes it trivial to configure timeouts in one place (design) and one knob at call sites.
- Always returns control to the workflow in time to produce a final answer (finalizer turn).
- Avoids activities outliving the run window (including the very first Plan).
- Keeps nested agent-as-tool runs bounded by the parent’s timing.

### Desired Outcome

- DSL offers a single Timing block with three intuitive knobs:
  - Budget: total wall‑clock budget for the run.
  - Plan: timeout applied to both Plan and Resume activities.
  - Tools: default timeout for tool executions.
- Callers can optionally override all three with one structured option and optionally set per‑tool timeouts.
- Runtime caps every activity by the remaining run window:
  - Compute budgetEnd = now + Budget; hardDeadline = budgetEnd + finalizeWindow (internal).
  - EffectiveTimeout = min(configured phase/tool timeout, remaining to hardDeadline).
  - When remaining ≤ finalizeWindow, the runtime does not schedule more work and runs a finalizer turn.
- The very first Plan is also capped so the workflow can always finalize before TTL.
- Nested agent-as-tool runs inherit the parent hardDeadline (effective = min(parent, nested)).
- Engine TTL: RunTimeout = min(15m, Budget + finalizeWindow + headroom(5s)).
- Generator no longer hardcodes Plan/Resume timeouts; it renders DSL overrides or emits zero so runtime defaults apply.
- Observability logs and events clearly show resolved timing and effective timeouts.

---

## Implementation Plan (Ordered)

1) DSL: Add Timing block in agent RunPolicy
   - Timing(Budget("10m"), Plan("45s"), Tools("2m"))
   - Budget sets run policy TimeBudget.
   - Plan sets a Plan/Resume activity timeout override.
   - Tools sets default ExecuteTool activity timeout override.

2) Expr: Extend RunPolicyExpr to hold PlanTimeout and ToolTimeout
   - Add fields on RunPolicyExpr or an embedded Timing payload.
   - Wire DSL evaluation to set these fields.

3) Runtime: Introduce Timing override
   - Add runtime.Timing struct { Budget, Plan, Tools, PerToolTimeout map }.
   - Add WithTiming(t Timing) RunOption that maps to PolicyOverrides fields: TimeBudget, PlanTimeout, ToolTimeout, PerToolTimeout.

4) Runtime: Compute deadlines before first Plan and cap it
   - In ExecuteWorkflow, compute budgetDeadline and hardDeadline up front (using default internal finalize window).
   - Pass hardDeadline into the initial runPlanActivity instead of zero.
   - Apply Plan timeout override from PolicyOverrides when present.

5) Runtime: Ensure all activities are capped by remaining window
   - Confirm runPlanActivity and executeToolCalls cap req.Timeout by remaining to hardDeadline.
   - Ensure Resume and Tools paths already apply the cap consistently.

6) Runtime: Per-tool timeouts (caller option)
   - Extend executeToolCalls to apply per‑tool configured timeouts (from PolicyOverrides) before capping to hardDeadline.
   - Support simple wildcard/prefix matching (e.g., "atlas_data_agent.ada.*").

7) Runtime: Nested runs inherit parent deadlines
   - In ExecuteAgentInline and ExecuteAgentInlineWithRoute, compute effective deadline = min(parentHardDeadline, nestedBudget+nestedFinalizeWindow).
   - Pass effective deadline to nested Plan/Resume and cap all nested tool activities.

8) Generator: Stop hardcoding planner timeouts
   - Do not bake 2m Plan/Resume; emit zero unless DSL overrides are set.
   - Runtime defaults (Plan/Resume 30s, Tools 2m) fill in when unspecified.

9) Generator: Render DSL overrides when provided
   - If Plan(...) is set, assign PlanActivityOptions.Timeout and ResumeActivityOptions.Timeout.
   - If Tools(...) is set, assign ExecuteToolActivityOptions.Timeout.

10) Observability
   - At run start, log/emit: Budget, Plan, Tools, computed hardDeadline, and TTL.
   - For each activity, log effective timeout used.

11) Engine TTL
   - Keep TTL computation as min(15m, Budget + finalizeWindow + headroom 5s).
   - finalizeWindow and headroom remain internal (hidden from DSL).

12) Documentation and examples
   - Update docs and examples to show Timing in DSL and WithTiming in callers.

---

## Progress Tracker

- [x] 1) DSL: Timing block (Budget, Plan, Tools)
- [x] 2) Expr: RunPolicyExpr fields + evaluation wiring
- [x] 3) Runtime: Timing struct + WithTiming override
- [x] 4) Runtime: Cap initial Plan by computed hardDeadline
- [x] 5) Runtime: Cap all activities by remaining window
- [x] 6) Runtime: Per‑tool timeouts (caller override)
- [ ] 7) Runtime: Nested runs inherit parent deadlines
- [x] 8) Generator: Remove hardcoded planner timeouts
- [x] 9) Generator: Render DSL overrides into registry
- [x] 10) Observability: timing logs/events
- [x] 11) Engine TTL remains min(15m, Budget+window+headroom)
- [x] 12) Docs + examples updated


