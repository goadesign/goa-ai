// Package policy codifies policy evaluation and enforcement for agent runs.
// Policy engines decide which tools are available to planners on each turn,
// enforce resource caps (max tool calls, time budgets, failure limits), and
// react to planner retry hints. This allows runtime-level control over agent
// behavior without modifying planner logic or tool implementations.
package policy

import (
	"context"
	"time"

	"goa.design/goa-ai/agents/runtime/run"
)

type (
	// Engine decides which tools remain available to the planner on each turn.
	// The runtime invokes the policy engine before each planner call (start and resume)
	// to compute the allowlist and update caps. This enables dynamic tool filtering,
	// circuit breaking, and budget enforcement without planner awareness.
	//
	// Implementations can inspect retry hints, track failure patterns, consult external
	// systems (approval workflows, rate limiters), or apply rule-based logic to restrict
	// tool access. The default implementation (if no Engine is provided) allows all tools
	// and enforces basic cap counting.
	Engine interface {
		// Decide evaluates policy constraints and returns the decision for this turn.
		// The runtime passes candidate tools, remaining caps, retry hints, and context.
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

		// RetryHint carries planner suggestions after tool failures (e.g., "disable
		// this tool", "increase timeout"). Nil if no hint was provided. Policies
		// can honor or ignore these hints based on configuration.
		RetryHint *RetryHint

		// RemainingCaps reflects the current execution budgets (remaining tool calls,
		// consecutive failures allowed, time budget). Policies use this to decide
		// whether to allow more tool invocations or terminate the run.
		RemainingCaps CapsState

		// Requested enumerates tools explicitly requested by the caller or planner
		// (e.g., via caller override or planner-generated tool calls). Policies can
		// use this to prioritize or restrict requested tools.
		Requested []ToolHandle

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
		AllowedTools []ToolHandle

		// Caps carries the updated caps that should be enforced for this turn and
		// subsequent turns. Policies can decrement counts (consume budget) or adjust
		// limits based on observed behavior.
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

	// ToolMetadata describes a candidate tool available to the agent. The runtime
	// provides this metadata to the policy engine for filtering and allowlist decisions.
	ToolMetadata struct {
		// ID is the fully qualified tool identifier (e.g., "weather.search.forecast").
		// Format: <service>.<toolset>.<tool>.
		ID string

		// Name is the human-readable tool name (e.g., "Get Weather Forecast").
		// Used for UI display or logging.
		Name string

		// Description documents the tool's purpose and behavior. Policies may inspect
		// this for keyword-based filtering (e.g., block tools mentioning "delete").
		Description string

		// Tags lists metadata labels for filtering (e.g., ["privileged", "external"]).
		// Policies can allowlist/blocklist based on tags without hardcoding tool IDs.
		Tags []string
	}

	// ToolHandle identifies a tool by its fully qualified ID. Used in allowlists,
	// requested tool lists, and policy decisions to reference specific tools without
	// carrying full metadata.
	ToolHandle struct {
		// ID is the fully qualified tool identifier (matches ToolMetadata.ID).
		ID string
	}

	// CapsState tracks remaining execution budgets for a run. The runtime decrements
	// these counters as tool calls execute and failures occur. When caps are exhausted,
	// the runtime terminates the workflow or forces a final response.
	CapsState struct {
		// MaxToolCalls is the total allowed tool invocations for the run. Zero means
		// unlimited. Configured per-agent in the design via RunPolicy.
		MaxToolCalls int

		// RemainingToolCalls tracks how many tool invocations are still allowed. The
		// runtime decrements this after each tool execution (success or failure).
		// When this reaches zero, no more tool calls are permitted.
		RemainingToolCalls int

		// MaxConsecutiveFailedToolCalls caps consecutive failures per run. Zero means
		// unlimited. Used for circuit breaking: if N tools fail in a row, terminate.
		MaxConsecutiveFailedToolCalls int

		// RemainingConsecutiveFailedToolCalls tracks how many consecutive failures are allowed
		// before circuit breaking. The runtime decrements this on each failure and resets
		// it to MaxConsecutiveFailedToolCalls on success. When this reaches zero, the
		// run is terminated.
		RemainingConsecutiveFailedToolCalls int

		// ExpiresAt conveys when the run-level budgets expire (wall-clock deadline).
		// Zero means no deadline. The runtime terminates the workflow if time.Now()
		// exceeds this timestamp. Configured per-agent via RunPolicy.TimeBudget.
		ExpiresAt time.Time
	}
)

// RetryReason categorizes planner failures communicated via RetryHint. These values
// mirror planner.RetryReason so policy engines can share logic without importing the
// planner package, avoiding import cycles with hooks.
type RetryReason string

const (
	RetryReasonInvalidArguments  RetryReason = "invalid_arguments"
	RetryReasonMissingFields     RetryReason = "missing_fields"
	RetryReasonMalformedResponse RetryReason = "malformed_response"
	RetryReasonTimeout           RetryReason = "timeout"
	RetryReasonRateLimited       RetryReason = "rate_limited"
	RetryReasonToolUnavailable   RetryReason = "tool_unavailable"
)

// RetryHint communicates planner guidance after tool failures so policy engines can
// adjust allowlists or caps. The runtime converts planner retry hints into this type
// before invoking Engine.Decide.
type RetryHint struct {
	Reason             RetryReason
	Tool               string
	RestrictToTool     bool
	MissingFields      []string
	ExampleInput       map[string]any
	PriorInput         map[string]any
	ClarifyingQuestion string
	Message            string
}
