package dsl

import (
	"time"

	expragents "goa.design/goa-ai/expr/agents"
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

// DefaultCaps defines the per-run capability limits for the current agent
// policy, configuring resource ceilings such as the maximum number of tool
// calls or allowed consecutive failures during each agent execution. Place
// DefaultCaps inside a RunPolicy block to specify these constraints.
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

// TimeBudget sets the maximum wall-clock duration the agent is allowed to run per execution.
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

// InterruptsAllowed toggles whether runtime interrupts are honored.
func InterruptsAllowed(allowed bool) {
	policy, ok := eval.Current().(*expragents.RunPolicyExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	policy.InterruptsAllowed = allowed
}

// CapsOption defines a functional option for configuring per-run resource limits
// on agent execution.
type CapsOption func(*expragents.CapsExpr)

// MaxToolCalls returns a CapsOption that sets the maximum number of tool invocations.
func MaxToolCalls(n int) CapsOption {
	return func(c *expragents.CapsExpr) {
		c.MaxToolCalls = n
	}
}

// MaxConsecutiveFailedToolCalls sets the maximum number of consecutive failed tool invocations.
func MaxConsecutiveFailedToolCalls(n int) CapsOption {
	return func(c *expragents.CapsExpr) {
		c.MaxConsecutiveFailedToolCall = n
	}
}
