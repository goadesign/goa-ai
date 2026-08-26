// Package runtime owns the run-level budget for planner activities scheduled to
// replace rejected tool or model output.
package runtime

import "goa.design/goa-ai/runtime/agent/policy"

// initialCaps constructs the initial caps state from the agent's run policy.
// Remaining counts mirror the configured maximums. A missing recovery setting
// receives the framework default.
func initialCaps(cfg RunPolicy) policy.CapsState {
	maxRecoveryTurns := cfg.MaxRecoveryTurns
	if maxRecoveryTurns == 0 {
		maxRecoveryTurns = policy.DefaultMaxRecoveryTurns
	}
	return policy.CapsState{
		MaxToolCalls:           cfg.MaxToolCalls,
		RemainingToolCalls:     cfg.MaxToolCalls,
		MaxRecoveryTurns:       maxRecoveryTurns,
		RemainingRecoveryTurns: maxRecoveryTurns,
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

// hasSuccessfulBudgetedResult reports whether a step completed domain work.
// Bookkeeping results do not end a recovery episode.
func (r *Runtime) hasSuccessfulBudgetedResult(records []stepToolRecord) bool {
	for _, record := range records {
		if record.result == nil || r.isBookkeeping(record.call.Name) {
			continue
		}
		if record.result.Failure == nil {
			return true
		}
	}
	return false
}

// consumeRecoveryTurn reserves one replacement planner activity from the
// positive maximum materialized by initialCaps.
func consumeRecoveryTurn(caps *policy.CapsState) bool {
	if caps.RemainingRecoveryTurns == 0 {
		return false
	}
	caps.RemainingRecoveryTurns--
	return true
}

// resetRecoveryTurns starts a fresh recovery episode after successful budgeted
// tool work.
func resetRecoveryTurns(caps *policy.CapsState) {
	caps.RemainingRecoveryTurns = caps.MaxRecoveryTurns
}
