// Package runtime centralizes the timeout contract shared by run submission and
// workflow execution so the engine-level run timeout can never undercut the
// workflow's own hard deadline.
package runtime

import "time"

const (
	// defaultEngineRunTimeout bounds runs that do not declare an explicit policy budget.
	defaultEngineRunTimeout = 15 * time.Minute

	// runTimeoutHeadroom leaves a small buffer after the hard workflow deadline so
	// the engine timeout does not preempt deterministic terminal hook emission.
	runTimeoutHeadroom = 30 * time.Second
)

type runTiming struct {
	TimeBudget     time.Duration
	FinalizerGrace time.Duration
	RunTimeout     time.Duration
}

// resolveRunTiming derives the workflow timing contract shared by startRunOn and
// ExecuteWorkflow.
//
// Contract:
//   - TimeBudget governs active planner/tool execution. Zero means "use the engine
//     default run timeout" rather than introducing an unbounded workflow.
//   - FinalizerGrace always reserves enough time for one final planner resume turn.
//   - RunTimeout is the engine-level wall clock bound and must never undercut the
//     workflow hard deadline computed from TimeBudget + FinalizerGrace.
func resolveRunTiming(reg AgentRegistration, input *RunInput) runTiming {
	timing := runTiming{
		RunTimeout: defaultEngineRunTimeout,
	}
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

	resumeTimeout := reg.ResumeActivityOptions.StartToCloseTimeout
	if input != nil && input.Policy != nil && input.Policy.PlanTimeout > 0 {
		resumeTimeout = input.Policy.PlanTimeout
	}
	if resumeTimeout == 0 {
		resumeTimeout = defaultResumeActivityTimeout
	}
	if timing.FinalizerGrace < resumeTimeout {
		timing.FinalizerGrace = resumeTimeout
	}
	if timing.TimeBudget > 0 {
		timing.RunTimeout = timing.TimeBudget + timing.FinalizerGrace + runTimeoutHeadroom
	}
	return timing
}
