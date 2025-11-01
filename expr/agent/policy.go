package agent

import (
	"fmt"
	"time"

	"goa.design/goa/v3/eval"
)

type (
	// RunPolicyExpr defines runtime execution and resource constraints for
	// a single agent.
	RunPolicyExpr struct {
		eval.DSLFunc

		// Agent is the agent expression this policy applies to.
		Agent *AgentExpr
		// DefaultCaps specifies default per-run limits on tool usage.
		DefaultCaps *CapsExpr
		// TimeBudget is the maximum duration a run may execute before
		// being terminated.
		TimeBudget time.Duration
		// InterruptsAllowed indicates whether the agent can be
		// interrupted during execution.
		InterruptsAllowed bool
	}

	// CapsExpr defines per-run limits on agent tool usage.
	CapsExpr struct {
		// Policy is the run policy expression this caps configuration
		// belongs to.
		Policy *RunPolicyExpr
		// MaxToolCalls is the maximum number of tool invocations
		// allowed in a single run.
		MaxToolCalls int
		// MaxConsecutiveFailedToolCall is the maximum number of
		// consecutive tool failures before the run is terminated.
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
