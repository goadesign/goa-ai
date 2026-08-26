package dsl

import (
	"time"

	expragents "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// RunPolicy defines execution constraints for the current agent. Use RunPolicy
// to configure resource limits, timeouts, history management, and runtime
// behaviors that govern how the agent executes. These policies are enforced by
// the runtime during agent execution.
//
// RunPolicy must appear in an Agent expression.
//
// RunPolicy takes a single argument which is the defining DSL function.
//
// The DSL function may use:
//   - DefaultCaps to set capability limits (tool calls, recovery turns)
//   - TimeBudget to set maximum execution duration
//   - OnMissingFields to configure validation behavior
//   - History to configure how conversation history is truncated or compressed
//   - Cache to configure prompt caching hints for supported providers
//
// Example:
//
//	Agent("assistant", "Helper agent", func() {
//	    RunPolicy(func() {
//	        DefaultCaps(MaxToolCalls(10), MaxRecoveryTurns(3))
//	        TimeBudget("5m")
//	        OnMissingFields("await_clarification")
//	        History(func() {
//	            KeepRecentTurns(20)
//	        })
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

// DefaultCaps configures independent resource limits for agent execution. Use
// it to control how many tools the agent can invoke and how many replacement
// planner activities may follow rejected tool or model output.
//
// DefaultCaps must appear in a RunPolicy expression.
//
// DefaultCaps takes zero or more CapsOption arguments (created via MaxToolCalls
// and MaxRecoveryTurns).
//
// Example:
//
//	RunPolicy(func() {
//	    DefaultCaps(
//	        MaxToolCalls(20),
//	        MaxRecoveryTurns(3),
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

// TimeBudget sets the active-time budget for planner and tool work.
// External-input waits pause this budget, and final planner work uses a
// separate grace window.
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

// OnMissingFields configures how the agent responds when tool invocation
// validation detects missing required fields. This allows you to control
// whether the agent should stop, request user input, or continue execution.
//
// OnMissingFields must appear in a RunPolicy expression.
//
// OnMissingFields takes a single string argument. Valid values:
//   - "finalize": stop execution when required fields are missing
//   - "await_clarification": end with a request for the missing information
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

// MaxToolCalls configures the maximum number of budgeted (non-bookkeeping) tool
// invocations allowed during agent execution. Use this with DefaultCaps to
// limit retrieval-style tool usage while exempting bookkeeping calls.
//
// MaxToolCalls takes a single positive integer argument specifying the maximum count.
//
// Example:
//
//	DefaultCaps(MaxToolCalls(15))
func MaxToolCalls(n int) CapsOption {
	return func(c *expragents.CapsExpr) {
		if n <= 0 {
			eval.ReportError("MaxToolCalls requires n > 0")
			return
		}
		c.MaxToolCalls = n
	}
}

// MaxRecoveryTurns configures the maximum number of consecutive additional
// planner activities the runtime may schedule after rejected tool or model
// output. Successful budgeted tool work starts a fresh recovery episode. The
// runtime allows three turns when this option is omitted.
//
// MaxRecoveryTurns takes a single positive integer argument specifying
// the number of replacement planner activities.
//
// Example:
//
//	DefaultCaps(MaxRecoveryTurns(3))
func MaxRecoveryTurns(n int) CapsOption {
	return func(c *expragents.CapsExpr) {
		if n <= 0 {
			eval.ReportError("MaxRecoveryTurns requires n > 0")
			return
		}
		c.MaxRecoveryTurns = n
	}
}
