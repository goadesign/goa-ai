package expr

import (
	"fmt"
	"time"
)

type (
	// RunPolicyExpr defines runtime execution and resource constraints for a single agent.
	//
	// It holds configuration for resource caps (such as maximum tool calls per run),
	// time budgets, and control over whether external interruptions are allowed. This
	// policy is enforced for each agent instance to ensure robust operation and to
	// protect the service from overuse or runaway scenarios.
	RunPolicyExpr struct {
		// Agent is the owner of this run policy. It always points to the agent this policy applies to.
		Agent *AgentExpr

		// DefaultCaps defines the default per-run resource limits (such as max tool calls and failures) for the agent.
		// If nil, there are no default caps enforced for this agent.
		DefaultCaps *CapsExpr

		// TimeBudget is the maximum allowed duration for the agent's execution per run.
		// A zero value means unlimited execution time (no time limit).
		TimeBudget time.Duration

		// InterruptsAllowed indicates whether external interruptions are permitted for the agent during its run
		// (e.g., cancellation via API or operator intervention).
		InterruptsAllowed bool
	}

	// CapsExpr defines per-run limits on agent tool usage to restrict resource consumption
	// and prevent excessive or runaway tool calls within a single agent execution.
	//
	// All fields are optional; if nil, no limit is enforced for that cap. Enforcement is handled
	// at runtime using Goa policies and runtime checks. Use Clone to safely copy caps.
	CapsExpr struct {
		// Policy is the parent run policy.
		Policy *RunPolicyExpr

		// MaxToolCalls restricts the maximum total number of tool invocations allowed per run.
		// If nil, there is no limit.
		MaxToolCalls int

		// MaxConsecutiveFailedToolCall limits the allowed number of consecutive tool call
		// failures before the agent halts or is considered as stalled. If nil, there is no such limit.
		MaxConsecutiveFailedToolCall int
	}
)

// EvalName returns a descriptive identifier for error reporting.
func (r *RunPolicyExpr) EvalName() string {
	return fmt.Sprintf("run policy for agent %q", r.Agent.Name)
}

// EvalName returns a descriptive identifier for error reporting.
func (c *CapsExpr) EvalName() string {
	return fmt.Sprintf("caps for agent %q", c.Policy.Agent.Name)
}
