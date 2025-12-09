package dsl

import (
	"time"

	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// Timing groups timing configuration for an agent's run. Use Timing inside a
// RunPolicy to configure fine-grained timeouts for different phases of execution.
//
// Timing provides a cleaner way to organize multiple duration settings compared to
// calling Budget, Plan, and Tools directly inside RunPolicy. Both approaches are
// valid and produce equivalent configurations.
//
// Timing must appear inside a RunPolicy expression.
//
// Available settings inside Timing:
//   - Budget: total wall-clock time budget for the entire run
//   - Plan: timeout for Plan and Resume planner activities
//   - Tools: default timeout for tool execution activities
//
// Example:
//
//	RunPolicy(func() {
//	    Timing(func() {
//	        Budget("10m")  // Total run time
//	        Plan("45s")    // Planner timeout
//	        Tools("2m")    // Tool timeout
//	    })
//	})
//
// Without Timing grouping (equivalent):
//
//	RunPolicy(func() {
//	    TimeBudget("10m")  // Note: Budget and TimeBudget are equivalent
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

// Budget sets the total wall-clock budget for an agent run. The runtime will
// cancel the run if execution exceeds this duration. Budget is equivalent to
// TimeBudget and can be used either inside Timing or directly inside RunPolicy.
//
// Budget must appear inside a RunPolicy or Timing expression.
//
// Budget takes a single argument which is a Go duration string. Valid formats
// include combinations of hours, minutes, seconds, and milliseconds:
//   - "30s" — 30 seconds
//   - "5m" — 5 minutes
//   - "2h30m" — 2 hours 30 minutes
//   - "1m30s" — 1 minute 30 seconds
//
// Choose budgets that match your downstream SLAs. Consider network latency,
// model response times, and the complexity of tool operations.
//
// Example:
//
//	RunPolicy(func() {
//	    Timing(func() {
//	        Budget("10m")
//	    })
//	})
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

// Plan sets the timeout for PlanStart and PlanResume planner activities. These
// activities invoke your planner implementation and typically involve LLM API
// calls. If the planner does not respond within this duration, the activity is
// retried according to the retry policy.
//
// Plan must appear inside a RunPolicy or Timing expression.
//
// Plan takes a single argument which is a Go duration string (e.g., "30s", "1m",
// "2m30s"). Choose a timeout that accommodates your model provider's typical
// response latency plus a safety margin for retries.
//
// Example:
//
//	RunPolicy(func() {
//	    Timing(func() {
//	        Plan("45s")  // Allow 45 seconds for planner activities
//	    })
//	})
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

// Tools sets the default timeout for ExecuteTool activities. This timeout applies
// to individual tool invocations when the tool executor runs the underlying
// implementation (service method call, MCP request, or nested agent execution).
//
// Tools must appear inside a RunPolicy or Timing expression.
//
// Tools takes a single argument which is a Go duration string (e.g., "30s", "2m",
// "5m"). The appropriate timeout depends on your tool implementations:
//   - Fast lookups: 10-30 seconds
//   - Database queries: 30 seconds to 2 minutes
//   - External API calls: 1-5 minutes
//   - Long-running operations: 5+ minutes (consider async patterns)
//
// Example:
//
//	RunPolicy(func() {
//	    Timing(func() {
//	        Tools("2m")  // Allow 2 minutes per tool execution
//	    })
//	})
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
