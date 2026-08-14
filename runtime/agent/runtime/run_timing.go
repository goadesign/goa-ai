// Package runtime centralizes the timing contract shared by run submission and
// workflow execution.
//
// The workflow loop (workflow_loop.go) is the sole owner of run-duration
// enforcement. TimeBudget forms the Budget deadline for active planner/tool
// work. FinalizerGrace extends that into a Hard deadline for one final planner
// activity. An external-input request ends the workflow with the remaining
// Budget and Hard durations in its checkpoint. A continuation starts new
// deadlines from those durations, so user response time never burns the
// run's active-time budget.
//
// The engine (e.g. Temporal WorkflowRunTimeout) must never impose a second,
// competing wall-clock ceiling on top of this: unlike the workflow's own
// deadline check, an engine-level timeout force-closes the run from outside
// application code, so it can fire mid-turn before the runtime emits a
// canonical terminal event. Engine run-timeout fields exist
// for engines/callers that want one, but this runtime intentionally leaves
// them unset for every run it starts.
package runtime

import "time"

type runTiming struct {
	TimeBudget     time.Duration
	FinalizerGrace time.Duration
}

// resolveRunTiming derives the workflow timing contract shared by startRunOn and
// ExecuteWorkflow.
//
// Contract:
//   - TimeBudget governs active planner/tool execution only; zero means no
//     active-time budget (the run finalizes only via caps or a terminal tool).
//   - FinalizerGrace is the exact extension from Budget to Hard. Finalization
//     that starts before Budget may use the remaining Budget plus this grace.
//   - Neither value is ever projected onto an engine-level run timeout. Saved
//     continuations carry remaining active durations into a new workflow, so a
//     separate engine deadline would compete with this contract.
func resolveRunTiming(reg AgentRegistration, input *RunInput) runTiming {
	var timing runTiming
	if reg.Policy.TimeBudget > 0 {
		timing.TimeBudget = reg.Policy.TimeBudget
	}
	if input != nil && input.Policy != nil && input.Policy.TimeBudget > 0 {
		timing.TimeBudget = input.Policy.TimeBudget
	}

	switch {
	case input != nil && input.Policy != nil && input.Policy.FinalizerGrace > 0:
		timing.FinalizerGrace = input.Policy.FinalizerGrace
	case reg.Policy.FinalizerGrace > 0:
		timing.FinalizerGrace = reg.Policy.FinalizerGrace
	default:
		timing.FinalizerGrace = defaultFinalizerGrace
	}
	return timing
}
