package agent

import (
	"fmt"
	"strings"
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
		// OnMissingFields controls behavior when validation indicates
		// missing fields.  Allowed values: "finalize" |
		// "await_clarification" | "resume". Empty means unspecified.
		OnMissingFields string
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

// Validate enforces semantic constraints on the run policy.
func (r *RunPolicyExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if strings.TrimSpace(r.OnMissingFields) != "" {
		switch r.OnMissingFields {
		case "finalize", "await_clarification", "resume":
			// ok
		default:
			verr.Add(r, "invalid OnMissingFields value %q (allowed: finalize, await_clarification, resume)", r.OnMissingFields)
		}
		if r.OnMissingFields == "await_clarification" && !r.InterruptsAllowed {
			verr.Add(r, "OnMissingFields(\"await_clarification\") requires InterruptsAllowed(true)")
		}
	}
	return verr
}

// EvalName returns a descriptive identifier for error reporting.
func (c *CapsExpr) EvalName() string {
	return fmt.Sprintf("caps for agent %q", c.Policy.Agent.Name)
}
