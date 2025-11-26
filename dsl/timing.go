package dsl

import (
	"time"

	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// Timing defines run timing configuration for an agent:
//   - Budget: overall wall-clock
//   - Plan: timeout for both Plan and Resume activities
//   - Tools: default timeout for tool activities
//
// Example:
//
//	RunPolicy(func() {
//	    Timing(func() {
//	        Budget("10m")
//	        Plan("45s")
//	        Tools("2m")
//	    })
//	})
func Timing(fn func()) {
	policy, ok := eval.Current().(*agentsexpr.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if fn != nil {
		eval.Execute(fn, policy)
	}
}

// Budget sets the total wall-clock budget for a run.
// Accepts Go duration strings (e.g., "30s", "5m").
func Budget(duration string) {
	policy, ok := eval.Current().(*agentsexpr.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	dur, err := time.ParseDuration(duration)
	if err != nil {
		eval.ReportError("invalid duration %q: %w", duration, err)
		return
	}
	policy.TimeBudget = dur
}

// Plan sets the timeout for both Plan and Resume activities.
// Accepts Go duration strings (e.g., "30s", "1m").
func Plan(duration string) {
	policy, ok := eval.Current().(*agentsexpr.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	dur, err := time.ParseDuration(duration)
	if err != nil {
		eval.ReportError("invalid duration %q: %w", duration, err)
		return
	}
	policy.PlanTimeout = dur
}

// Tools sets the default timeout for ExecuteTool activities.
// Accepts Go duration strings (e.g., "2m").
func Tools(duration string) {
	policy, ok := eval.Current().(*agentsexpr.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	dur, err := time.ParseDuration(duration)
	if err != nil {
		eval.ReportError("invalid duration %q: %w", duration, err)
		return
	}
	policy.ToolTimeout = dur
}
