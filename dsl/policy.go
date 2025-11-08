package dsl

import (
	"time"

	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// RunPolicy defines execution constraints for the current agent. Use RunPolicy
// to configure resource limits, timeouts, and runtime behaviors that govern how
// the agent executes. These policies are enforced by the runtime during agent
// execution.
//
// RunPolicy must appear in an Agent expression.
//
// RunPolicy takes a single argument which is the defining DSL function.
//
// The DSL function may use:
//   - DefaultCaps to set capability limits (tool calls, consecutive failures)
//   - TimeBudget to set maximum execution duration
//   - InterruptsAllowed to enable or disable user interruptions
//   - OnMissingFields to configure validation behavior
//
// Example:
//
//	Agent("assistant", "Helper agent", func() {
//	    RunPolicy(func() {
//	        DefaultCaps(MaxToolCalls(10), MaxConsecutiveFailedToolCalls(3))
//	        TimeBudget("5m")
//	        InterruptsAllowed(true)
//	        OnMissingFields("await_clarification")
//	    })
//	})
func RunPolicy(fn func()) {
	agent, ok := eval.Current().(*expragents.AgentExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	policy := agent.RunPolicy
	if policy == nil {
		policy = &expragents.RunPolicyExpr{
			Agent: agent,
		}
		agent.RunPolicy = policy
	}
	if fn != nil {
		eval.Execute(fn, policy)
	}
}

// DefaultCaps configures resource limits for agent execution. Use DefaultCaps
// to control how many tools the agent can invoke and how many consecutive
// failures are tolerated before stopping execution.
//
// DefaultCaps must appear in a RunPolicy expression.
//
// DefaultCaps takes zero or more CapsOption arguments (created via MaxToolCalls
// and MaxConsecutiveFailedToolCalls).
//
// Example:
//
//	RunPolicy(func() {
//	    DefaultCaps(
//	        MaxToolCalls(20),
//	        MaxConsecutiveFailedToolCalls(3),
//	    )
//	})
func DefaultCaps(opts ...CapsOption) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	caps := policy.DefaultCaps
	if caps == nil {
		caps = &expragents.CapsExpr{Policy: policy}
		policy.DefaultCaps = caps
	}
	for _, opt := range opts {
		if opt != nil {
			opt(caps)
		}
	}
}

// TimeBudget sets the maximum execution time for the agent. The agent will be
// stopped if it exceeds this duration.
//
// TimeBudget must appear in a RunPolicy expression.
//
// TimeBudget takes a single argument which is a duration string (e.g., "30s",
// "5m", "1h").
//
// Example:
//
//	RunPolicy(func() {
//	    TimeBudget("5m")
//	})
func TimeBudget(duration string) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
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

// InterruptsAllowed configures whether user interruptions are permitted during
// agent execution. When enabled, users can interrupt running agents to provide
// guidance or stop execution.
//
// InterruptsAllowed must appear in a RunPolicy expression.
//
// InterruptsAllowed takes a single boolean argument.
//
// Example:
//
//	RunPolicy(func() {
//	    InterruptsAllowed(true)
//	})
func InterruptsAllowed(allowed bool) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	policy.InterruptsAllowed = allowed
}

// OnMissingFields configures how the agent responds when tool invocation
// validation detects missing required fields. This allows you to control
// whether the agent should stop, wait for user input, or continue execution.
//
// OnMissingFields must appear in a RunPolicy expression.
//
// OnMissingFields takes a single string argument. Valid values:
//   - "finalize": stop execution when required fields are missing
//   - "await_clarification": pause and wait for user to provide missing information
//   - "resume": continue execution despite missing fields
//   - "" (empty): let the planner decide based on context
//
// Example:
//
//	RunPolicy(func() {
//	    OnMissingFields("await_clarification")
//	})
func OnMissingFields(action string) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	switch action {
	case "", "finalize", "await_clarification", "resume":
		policy.OnMissingFields = action
	default:
		eval.ReportError("invalid OnMissingFields value %q (allowed: finalize, await_clarification, resume)", action)
	}
}

// CapsOption defines a functional option for configuring per-run resource limits
// on agent execution.
type CapsOption func(*expragents.CapsExpr)

// MaxToolCalls configures the maximum number of tool invocations allowed during
// agent execution. Use this with DefaultCaps to limit total tool usage.
//
// MaxToolCalls takes a single integer argument specifying the maximum count.
//
// Example:
//
//	DefaultCaps(MaxToolCalls(15))
func MaxToolCalls(n int) CapsOption {
	return func(c *expragents.CapsExpr) {
		c.MaxToolCalls = n
	}
}

// MaxConsecutiveFailedToolCalls configures the maximum number of consecutive
// tool failures before the agent stops execution. Use this with DefaultCaps to
// prevent runaway failure loops.
//
// MaxConsecutiveFailedToolCalls takes a single integer argument specifying the
// maximum consecutive failure count.
//
// Example:
//
//	DefaultCaps(MaxConsecutiveFailedToolCalls(3))
func MaxConsecutiveFailedToolCalls(n int) CapsOption {
	return func(c *expragents.CapsExpr) {
		c.MaxConsecutiveFailedToolCall = n
	}
}
