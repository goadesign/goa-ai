// Package policy codifies policy evaluation and enforcement for agent runs.
// Policy engines decide which tools are available to planners on each turn,
// and enforce tool-call and recovery-turn limits. Recovery is an execution
// transition owned by the runtime, not a policy suggestion.
package policy

import (
	"context"

	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// Engine decides which tools remain available to the planner on each turn.
	// The runtime invokes the policy engine before each planner call (start and resume)
	// to compute the allowlist and update caps. This enables dynamic tool filtering,
	// circuit breaking, and budget enforcement without planner awareness.
	//
	// Implementations can track failure patterns, consult external systems
	// (approval workflows, rate limiters), or apply rule-based logic to restrict
	// tool access. The default implementation (if no Engine is provided) allows
	// all tools and enforces basic cap counting.
	Engine interface {
		// Decide evaluates policy constraints and returns the decision for this turn.
		// The runtime passes candidate tools, remaining caps, and context.
		// Returns an error if the policy engine fails (e.g., external system unavailable);
		// this typically terminates the workflow.
		//
		// Implementations should be fast (< 100ms) to avoid blocking planner execution.
		// Heavy operations (API calls, database lookups) should use caching or background
		// precomputation.
		Decide(ctx context.Context, input Input) (Decision, error)
	}

	// Input groups all the information made available to the policy engine for
	// decision making. The runtime constructs this before each planner invocation.
	Input struct {
		// RunContext carries run-level identifiers, labels, and caps configuration.
		// Policies can inspect labels for routing decisions (e.g., allow privileged
		// tools for "admin" runs).
		RunContext run.Context

		// Tools lists all candidate tools allowed by the agent design and runtime
		// registration. The policy engine filters this list down to the allowlist
		// for the current turn.
		Tools []ToolMetadata

		// RemainingCaps reflects the current execution budgets (tool calls,
		// recovery turns, and time). Policies use this to decide
		// whether to allow more tool invocations or terminate the run.
		RemainingCaps CapsState

		// Requested enumerates tools explicitly requested by the caller or planner
		// (e.g., via caller override or planner-generated tool calls). Policies can
		// use this to prioritize or restrict requested tools.
		Requested []tools.Ident

		// Labels are arbitrary key/value pairs propagated to policy decisions. These
		// come from the RunContext or may be augmented by prior policy decisions.
		// Example: {"environment": "production", "user_tier": "premium"}.
		Labels map[string]string
	}

	// Decision captures the outcome of a policy evaluation for a turn. The runtime
	// applies this decision before invoking the planner: it filters tools to the
	// allowlist, updates caps, and may terminate the run if DisableTools is true.
	Decision struct {
		// AllowedTools is the final allowlist of tools for this turn. The runtime
		// ensures planners can only invoke tools in this list. Empty means no tools
		// are allowed (planner must produce a final response).
		AllowedTools []tools.Ident

		// Caps carries the updated caps that should be enforced for this turn and
		// subsequent turns. Policies may only tighten configured budgets and
		// deadlines; they never create new caps or relax existing ones.
		Caps CapsState

		// DisableTools signals that no further tool calls should be executed for this
		// run. If true, the runtime forces the planner to produce a final response or
		// terminates with an error. Used for circuit breaking or budget exhaustion.
		DisableTools bool

		// Labels allows policies to annotate downstream telemetry, memory, or hooks.
		// These labels are merged into the RunContext and propagated to subsequent
		// turns. Example: {"policy_applied": "failure_circuit_breaker"}.
		Labels map[string]string

		// Metadata captures policy-specific information (e.g., reason codes, approval IDs)
		// that should be persisted for audit trails or surfaced via hooks. The runtime
		// stores this alongside run records and emits it in policy decision events.
		Metadata map[string]any
	}

	// ToolBudgetClass classifies whether a tool consumes the run-level
	// MaxToolCalls budget.
	ToolBudgetClass string

	// ToolMetadata describes a candidate tool available to the agent. The runtime
	// provides this metadata to the policy engine for filtering and allowlist decisions.
	ToolMetadata struct {
		// ID is the fully qualified tool identifier (e.g., "weather.search.forecast").
		// Format: <service>.<toolset>.<tool>.
		ID tools.Ident

		// Title is a human-readable display title (e.g., "Get Weather Forecast").
		// It is presentation-only; policy engines should make decisions based on
		// ID and Tags. Codegen derives a sensible default from the tool name and
		// the DSL can optionally override it.
		Title string

		// Description documents the tool's purpose and behavior. Policies may inspect
		// this for keyword-based filtering (e.g., block tools mentioning "delete").
		Description string

		// Tags lists metadata labels for filtering (e.g., ["privileged", "external"]).
		// Policies can allowlist/blocklist based on tags without hardcoding tool IDs.
		Tags []string

		// BudgetClass reports whether invoking the tool consumes the run-level
		// MaxToolCalls budget. Budgeted tools count against the cap; bookkeeping
		// tools are exempt.
		BudgetClass ToolBudgetClass
	}

	// CapsState tracks remaining execution budgets for a run. The runtime
	// decrements these counters as tool calls execute or replacement planner
	// activities run. When caps are exhausted, the runtime finishes the run.
	CapsState struct {
		// MaxToolCalls is the total allowed budgeted tool invocations for the run.
		// Bookkeeping tools are exempt. Zero means the cap is not configured.
		// Configured per-agent in the design via RunPolicy.
		MaxToolCalls int

		// RemainingToolCalls tracks how many budgeted tool invocations are still
		// allowed. The runtime decrements this after each budgeted tool execution
		// (success or failure). When this reaches zero, no more budgeted tool calls
		// are permitted.
		RemainingToolCalls int

		// MaxRecoveryTurns caps consecutive additional planner activities
		// scheduled after rejected tool or model output. Zero selects the
		// runtime default.
		MaxRecoveryTurns int `json:"MaxConsecutiveFailedToolCalls,omitempty"` //nolint:tagliatelle // Historical Temporal state field.

		// RemainingRecoveryTurns tracks how many replacement planner activities
		// may still be scheduled in the current recovery episode. Successful
		// budgeted tool work resets it to MaxRecoveryTurns.
		RemainingRecoveryTurns int `json:"RemainingConsecutiveFailedToolCalls,omitempty"` //nolint:tagliatelle // Historical Temporal state field.
	}
)

const (
	// ToolBudgetClassBudgeted identifies tools that consume MaxToolCalls budget.
	ToolBudgetClassBudgeted ToolBudgetClass = "budgeted"
	// ToolBudgetClassBookkeeping identifies tools that are exempt from MaxToolCalls.
	ToolBudgetClassBookkeeping ToolBudgetClass = "bookkeeping"
)
