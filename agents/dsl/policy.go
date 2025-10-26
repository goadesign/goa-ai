package dsl

import (
	"time"

	"goa.design/goa-ai/agents/expr"
	"goa.design/goa/v3/eval"
)

// RunPolicy defines the agent run policy DSL, used to set runtime limits and
// constraints (such as capability budgets or timeout ceilings) for the current
// agent declaration.  Place this inside an Agent block to specify execution
// boundaries and limits enforced during agent runs. The policy is
// surface-level, configuring the agent's resource consumption contract during
// evaluation and orchestration.
//
// RunPolicy must appear in a Agent expression.
//
// RunPolicy takes a function that defines the run policy. The function can use
// the following functions to define the run policy:
//   - DefaultCaps: sets the default capability limits for the agent run
//   - TimeBudget: sets the time budget for the agent run
//   - InterruptsAllowed: sets whether user-initiated interruptions are allowed
//     during the agent run
//
// Example:
//
//	Agent("docs-agent", "Agent for managing documentation workflows", func() {
//		RunPolicy(func() {
//			DefaultCaps(MaxToolCalls(5), MaxConsecutiveFailedToolCalls(2))
//			TimeBudget("30s")
//			InterruptsAllowed(true)
//		})
//	})
func RunPolicy(fn func()) {
	agent, ok := eval.Current().(*expr.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	policy := agent.RunPolicy
	if policy == nil {
		policy = &expr.RunPolicyExpr{
			Agent: agent,
		}
		agent.RunPolicy = policy
	}
	if fn != nil {
		eval.Execute(fn, policy)
	}
}

// DefaultCaps defines the per-run capability limits for the current agent
// policy, configuring resource ceilings such as the maximum number of tool
// calls or allowed consecutive failures during each agent execution. Place
// DefaultCaps inside a RunPolicy block to specify these constraints.
//
// DefaultCaps must appear in a RunPolicy expression.
//
// DefaultCaps takes one or more CapsOption values, which set specific caps such
// as MaxToolCalls and MaxConsecutiveFailedToolCalls for the agent's run policy.
//
// Example:
//
//	RunPolicy(func() {
//		DefaultCaps(MaxToolCalls(5), MaxConsecutiveFailedToolCalls(2))
//	})
func DefaultCaps(opts ...CapsOption) {
	policy, ok := eval.Current().(*expr.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	caps := policy.DefaultCaps
	if caps == nil {
		caps = &expr.CapsExpr{Policy: policy}
		policy.DefaultCaps = caps
	}
	for _, opt := range opts {
		if opt != nil {
			opt(caps)
		}
	}
}

// TimeBudget sets the maximum wall-clock duration the agent is allowed to run
// per execution.  When the time budget is exceeded, the agent will halt and
// return a final response.
//
// TimeBudget must appear inside a RunPolicy expression.
//
// Pass a string duration (e.g., "60s", "2m", "1h", "2m45s") to specify the time
// budget for the agent's loop per run. The syntax is the same as Go's
// time.ParseDuration.
//
// Example:
//
//	RunPolicy(func() {
//		TimeBudget("30s")
//	})
func TimeBudget(duration string) {
	policy, ok := eval.Current().(*expr.RunPolicyExpr)
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

// InterruptsAllowed toggles whether runtime interrupts are honored.  When
// enabled, an agent run can be interrupted by external requests, in particular,
// this allows human-in-the-loop prompts to be surfaced to the caller.
//
// InterruptsAllowed must appear inside a RunPolicy expression.
//
// Pass a boolean value to specify whether runtime interrupts are honored.
//
// Example:
//
//	RunPolicy(func() {
//		InterruptsAllowed(true)
//	})
func InterruptsAllowed(allowed bool) {
	policy, ok := eval.Current().(*expr.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	policy.InterruptsAllowed = allowed
}

// CapsOption defines a functional option for configuring per-run resource limits
// on agent execution.
//
// CapsOption is used with DefaultCaps to set constraints such as the maximum
// allowed tool calls or consecutive failures per agent run. Each CapsOption
// modifies a given CapsExpr instance with the specified cap.
//
// Example usage:
//
//	DefaultCaps(MaxToolCalls(10), MaxConsecutiveFailedToolCalls(3))
//
// Capabilities supported:
//
//   - MaxToolCalls: Restricts the total number of tool invocations allowed per
//     run.
//   - MaxConsecutiveFailedToolCalls: Limits consecutive failed tool invocations
//     allowed per run.
//
// CapsOption is not intended to be used outside DSL configuration.
// See also: MaxToolCalls, MaxConsecutiveFailedToolCalls.
type CapsOption func(*expr.CapsExpr)

// MaxToolCalls returns a CapsOption that sets the maximum number of tool
// invocations allowed per agent run. If set, the agent will halt if this
// number is exceeded. Use with DefaultCaps to enforce per-run limits.
//
// Example:
//
//	DefaultCaps(MaxToolCalls(10))
//
// See also: CapsOption.
func MaxToolCalls(n int) CapsOption {
	return func(c *expr.CapsExpr) {
		c.MaxToolCalls = n
	}
}

// MaxConsecutiveFailedToolCalls returns a CapsOption that sets the maximum
// number of consecutive failed tool invocations allowed per agent run. If set,
// the agent will halt and return a final response if this number is exceeded
// during a run. Use with DefaultCaps to enforce limits on failed tool calls.
//
// Example:
//
//	DefaultCaps(MaxConsecutiveFailedToolCalls(3))
//
// See also: CapsOption.
func MaxConsecutiveFailedToolCalls(n int) CapsOption {
	return func(c *expr.CapsExpr) {
		c.MaxConsecutiveFailedToolCall = n
	}
}
