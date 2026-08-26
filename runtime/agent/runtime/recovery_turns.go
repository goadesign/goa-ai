// Package runtime owns the run-level budget for planner activities scheduled to
// replace rejected tool or model output. It also preserves the failed-batch
// counter used by older Temporal histories.
package runtime

import "goa.design/goa-ai/runtime/agent/policy"

const (
	// defaultMaxRecoveryTurns stops an unconfigured agent from retrying rejected
	// output forever.
	defaultMaxRecoveryTurns = 3
)

// initialCaps constructs the initial caps state from the agent's run policy.
// Remaining counts mirror the configured maximums. A missing recovery setting
// receives the framework default.
func initialCaps(cfg RunPolicy) policy.CapsState {
	maxRecoveryTurns := cfg.MaxRecoveryTurns
	if maxRecoveryTurns == 0 {
		maxRecoveryTurns = defaultMaxRecoveryTurns
	}
	return policy.CapsState{
		MaxToolCalls:           cfg.MaxToolCalls,
		RemainingToolCalls:     cfg.MaxToolCalls,
		MaxRecoveryTurns:       maxRecoveryTurns,
		RemainingRecoveryTurns: maxRecoveryTurns,
	}
}

// initialLegacyCaps preserves the unbounded zero value stored in workflow
// histories created before recovery turns had a safe default.
func initialLegacyCaps(cfg RunPolicy) policy.CapsState {
	return policy.CapsState{
		MaxToolCalls:           cfg.MaxToolCalls,
		RemainingToolCalls:     cfg.MaxToolCalls,
		MaxRecoveryTurns:       cfg.MaxRecoveryTurns,
		RemainingRecoveryTurns: cfg.MaxRecoveryTurns,
	}
}

// decrementCap decrements a cap value by delta. If current is 0 (cap not
// configured), it remains 0. If the result would be negative, the cap is
// exhausted and therefore clamped to 0.
func decrementCap(current, delta int) int {
	if current == 0 || delta == 0 {
		return current
	}
	return max(current-delta, 0)
}

// budgetedBatchOutcome classifies a step batch's budgeted (non-bookkeeping)
// results: progress reports at least one success and failed reports at least
// one failure.
func (r *Runtime) budgetedBatchOutcome(records []stepToolRecord) (progress, failed bool) {
	for _, record := range records {
		if record.result == nil || r.isBookkeeping(record.call.Name) {
			continue
		}
		if record.result.Failure != nil {
			failed = true
		} else {
			progress = true
		}
	}
	return progress, failed
}

// applyLegacyFailureStreak preserves the failed-batch counter used by workflow
// histories and version-four suspension records created before recovery turns.
func applyLegacyFailureStreak(caps *policy.CapsState, progress, failed bool) bool {
	switch {
	case progress:
		resetRecoveryTurns(caps)
	case failed:
		caps.RemainingRecoveryTurns = decrementCap(caps.RemainingRecoveryTurns, 1)
		return caps.MaxRecoveryTurns > 0 && caps.RemainingRecoveryTurns == 0
	}
	return false
}

// consumeRecoveryTurn reserves one replacement planner activity. A zero
// internal cap preserves uncapped state restored from older workflow history;
// every new run receives the configured or default limit in initialCaps.
func consumeRecoveryTurn(caps *policy.CapsState) bool {
	if caps.MaxRecoveryTurns == 0 {
		return true
	}
	if caps.RemainingRecoveryTurns == 0 {
		return false
	}
	caps.RemainingRecoveryTurns--
	return true
}

// resetRecoveryTurns starts a fresh recovery episode after successful budgeted
// tool work.
func resetRecoveryTurns(caps *policy.CapsState) {
	if caps.MaxRecoveryTurns > 0 {
		caps.RemainingRecoveryTurns = caps.MaxRecoveryTurns
	}
}
