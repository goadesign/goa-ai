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
		// PlanTimeout applies to both Plan and Resume activities when set.
		PlanTimeout time.Duration
		// ToolTimeout is the default ExecuteTool activity timeout when set.
		ToolTimeout time.Duration
		// InterruptsAllowed indicates whether the agent can be
		// interrupted during execution.
		InterruptsAllowed bool
		// OnMissingFields controls behavior when validation indicates
		// missing fields.  Allowed values: "finalize" |
		// "await_clarification" | "resume". Empty means unspecified.
		OnMissingFields string
		// History configures how the runtime prunes or compresses
		// conversational history before planner invocations.
		History *HistoryExpr
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

	// HistoryMode identifies which history policy is configured on an agent.
	HistoryMode string

	// HistoryExpr captures the design-time configuration for history
	// management. It encodes either a KeepRecentTurns or Compress
	// policy; at most one mode may be set.
	HistoryExpr struct {
		eval.DSLFunc

		// Policy is the run policy expression this history configuration
		// belongs to.
		Policy *RunPolicyExpr
		// Mode selects the history strategy.
		Mode HistoryMode
		// KeepRecent is the number of recent turns to retain when
		// ModeKeepRecent is selected.
		KeepRecent int
		// TriggerAt is the number of turns that must accumulate before
		// compression triggers when ModeCompress is selected.
		TriggerAt int
		// CompressKeepRecent is the number of recent turns to retain in
		// full fidelity when ModeCompress is selected.
		CompressKeepRecent int
	}
)

const (
	// HistoryModeKeepRecent configures a sliding-window policy that
	// retains only the most recent N turns.
	HistoryModeKeepRecent HistoryMode = "keep_recent"
	// HistoryModeCompress configures a summarization policy that
	// compresses older turns once a trigger threshold is reached.
	HistoryModeCompress HistoryMode = "compress"
)

// EvalName returns a descriptive identifier for error reporting.
func (r *RunPolicyExpr) EvalName() string {
	return fmt.Sprintf("run policy for agent %q", r.Agent.Name)
}

// Validate enforces semantic constraints on the run policy.
func (r *RunPolicyExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	if r.OnMissingFields != "" {
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
	if r.History != nil {
		switch r.History.Mode {
		case "":
			verr.Add(r.History, "history policy must specify a mode")
		case HistoryModeKeepRecent:
			if r.History.KeepRecent <= 0 {
				verr.Add(r.History, "KeepRecentTurns requires a positive turn count")
			}
		case HistoryModeCompress:
			if r.History.TriggerAt <= 0 {
				verr.Add(r.History, "Compress requires TriggerAt > 0")
			}
			if r.History.CompressKeepRecent < 0 {
				verr.Add(r.History, "Compress requires keepRecent >= 0")
			}
			if r.History.CompressKeepRecent >= r.History.TriggerAt {
				verr.Add(r.History, "Compress keepRecent must be less than TriggerAt")
			}
		default:
			verr.Add(r.History, "unknown history mode %q", r.History.Mode)
		}
	}
	return verr
}

// EvalName returns a descriptive identifier for error reporting.
func (h *HistoryExpr) EvalName() string {
	if h == nil || h.Policy == nil || h.Policy.Agent == nil {
		return "history policy"
	}
	return fmt.Sprintf("history policy for agent %q", h.Policy.Agent.Name)
}

// EvalName returns a descriptive identifier for error reporting.
func (c *CapsExpr) EvalName() string {
	return fmt.Sprintf("caps for agent %q", c.Policy.Agent.Name)
}
