// Package api defines shared types used across the agent runtime workflow and activity boundaries.
package api

import (
	"encoding/json"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// RunInput captures everything a generated workflow needs to start or resume a run.
// It ensures planners receive full conversational context plus caller-provided labels
// and metadata.
type RunInput struct {
	// AgentID identifies which agent should process the run.
	AgentID agent.Ident

	// RunID is the durable workflow execution identifier.
	RunID string

	// SessionID groups related runs (e.g., multi-turn conversations).
	SessionID string

	// TurnID identifies the conversational turn (optional). When set, all events
	// produced during this run are tagged with this TurnID for UI grouping.
	TurnID string

	// ParentToolCallID identifies the parent tool call when this run represents a
	// nested agent execution (agent-as-tool). Empty for top-level runs. Used to
	// correlate ToolCallUpdated events and propagate parent-child relationships.
	ParentToolCallID string

	// ParentRunID identifies the run that scheduled this nested execution. Empty for
	// top-level runs. When set, tool events emitted by this run can be attributed to
	// the parent run.
	ParentRunID string

	// ParentAgentID identifies the agent that invoked this nested execution. Empty for
	// top-level runs. When set with ParentRunID, tool events can retain the parent agent
	// identity even though execution happens in a child agent.
	ParentAgentID agent.Ident

	// Tool identifies the fully-qualified tool name when this run is a nested
	// agent-as-tool execution. For top-level runs (not invoked via a parent tool),
	// Tool is empty. Planners may use this to select method-specific prompts.
	// Format: "<service>.<toolset>.<tool>".
	Tool tools.Ident

	// ToolArgs carries the original JSON arguments for the parent tool when this run
	// is an agent-as-tool execution. Nil for top-level runs. Nested agent planners
	// can use this structured input to render method-specific prompts without
	// reparsing free-form messages.
	ToolArgs json.RawMessage

	// Messages carries the conversation history supplied by the caller.
	Messages []*model.Message

	// Labels contains caller-provided metadata (tenant, priority, etc.).
	Labels map[string]string

	// Metadata allows orchestrators to attach arbitrary structured data.
	Metadata map[string]any

	// WorkflowOptions carries engine-specific start options (memo, search attributes,
	// custom task queues). If nil, the runtime derives defaults from the agent registration.
	WorkflowOptions *WorkflowOptions

	// Policy carries optional per-run policy overrides applied on every planner turn.
	// These options allow callers to set caps and tool filters without modifying
	// the agent registration defaults.
	Policy *PolicyOverrides
}

// WorkflowOptions mirrors a subset of engine start options we expose through the runtime.
// Engine adapters convert these into native options at start-time.
type WorkflowOptions struct {
	// Memo is a map of key-value pairs that can be used to store data for the workflow.
	Memo map[string]any
	// SearchAttributes is a map of key-value pairs indexed by the engine for visibility.
	SearchAttributes map[string]any
	// TaskQueue is the name of the task queue to use for the workflow.
	TaskQueue string
	// RetryPolicy is the retry policy to use for the workflow start.
	RetryPolicy RetryPolicy
}

// RetryPolicy defines retry semantics shared by workflows and activities at the API layer.
type RetryPolicy struct {
	// MaxAttempts caps the total number of retry attempts. Zero means engine default.
	MaxAttempts int
	// InitialInterval is the delay before the first retry. Zero means engine default.
	InitialInterval time.Duration
	// BackoffCoefficient multiplies the delay after each retry (e.g., 2.0 for exponential).
	BackoffCoefficient float64
}

// PolicyOverrides configures per-run policy constraints. All fields are optional;
// zero values mean no override.
type PolicyOverrides struct {
	PerTurnMaxToolCalls           int
	RestrictToTool                tools.Ident
	AllowedTags                   []string
	DeniedTags                    []string
	MaxToolCalls                  int
	MaxConsecutiveFailedToolCalls int
	TimeBudget                    time.Duration
	PlanTimeout                   time.Duration
	ToolTimeout                   time.Duration
	PerToolTimeout                map[tools.Ident]time.Duration
	FinalizerGrace                time.Duration
	InterruptsAllowed             bool
}

// RunOutput represents the final outcome returned by a run workflow, including the
// concluding assistant message plus tool traces and planner notes for callers.
type RunOutput struct {
	// AgentID echoes the agent that produced the result.
	AgentID agent.Ident
	// RunID echoes the workflow execution identifier.
	RunID string
	// Final is the assistant reply returned to the caller.
	Final *model.Message
	// ToolEvents captures all tool results emitted before completion in execution order.
	ToolEvents []*planner.ToolResult
	// Notes aggregates planner annotations produced during the final turn.
	Notes []*planner.PlannerAnnotation
	// Usage aggregates model-reported token usage during the run when available.
	Usage *model.TokenUsage
}

// PlanActivityInput carries data for planner PlanStart/PlanResume activities.
type PlanActivityInput struct {
	AgentID     agent.Ident
	RunID       string
	Messages    []*model.Message
	RunContext  run.Context
	ToolResults []*planner.ToolResult
	Finalize    *planner.Termination
}

// PlanActivityOutput wraps the planner result produced by a plan/resume activity.
type PlanActivityOutput struct {
	Result     *planner.PlanResult
	Transcript []*model.Message
	Usage      model.TokenUsage
}

// HookActivityInput describes a hook event emitted from workflow code and
// published by the Hook activity. Payload contains the event-specific fields
// (excluding base metadata) encoded as JSON.
type HookActivityInput struct {
	// Type identifies the hook event variant (for example, hooks.ToolCallScheduled).
	Type hooks.EventType

	// RunID identifies the run that owns this event.
	RunID string

	// AgentID identifies the agent that owns this event.
	AgentID agent.Ident

	// SessionID identifies the logical session that owns this event.
	SessionID string

	// TurnID groups events for a single conversational turn. Empty when turn
	// tracking is disabled.
	TurnID string

	// Payload holds event-specific fields encoded as JSON.
	Payload json.RawMessage
}

// UnmarshalJSON handles decoding PlanActivityOutput so that Transcript entries are
// deserialized through the richer model.Message decoder (which materializes Part
// implementations). This keeps the workflow resilient to legacy payloads.
func (o *PlanActivityOutput) UnmarshalJSON(data []byte) error {
	type alias struct {
		Result     *planner.PlanResult `json:"Result"`     //nolint:tagliatelle
		Transcript []json.RawMessage   `json:"Transcript"` //nolint:tagliatelle
		Usage      model.TokenUsage    `json:"Usage"`      //nolint:tagliatelle
	}
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}

	o.Result = tmp.Result
	o.Usage = tmp.Usage
	if len(tmp.Transcript) == 0 {
		o.Transcript = nil
		return nil
	}

	out := make([]*model.Message, 0, len(tmp.Transcript))
	for i, raw := range tmp.Transcript {
		var msg model.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return fmt.Errorf("decode Transcript[%d]: %w", i, err)
		}
		out = append(out, &msg)
	}
	o.Transcript = out
	return nil
}

// ToolInput is the payload passed to tool executors. Payload is JSON-encoded.
type ToolInput struct {
	RunID            string
	AgentID          agent.Ident
	ToolsetName      string
	ToolName         tools.Ident
	ToolCallID       string
	Payload          json.RawMessage
	SessionID        string
	TurnID           string
	ParentToolCallID string
}

// ToolOutput is returned by tool executors after invoking the tool implementation.
type ToolOutput struct {
	Payload   json.RawMessage
	Artifacts []*planner.Artifact
	Telemetry *telemetry.ToolTelemetry
	Error     string
	RetryHint *planner.RetryHint
}

const (
	// SignalPause is the workflow signal name used to pause a run.
	SignalPause = "goaai.runtime.pause"

	// SignalResume is the workflow signal name used to resume a paused run.
	SignalResume = "goaai.runtime.resume"

	// SignalProvideClarification delivers a ClarificationAnswer to a waiting run.
	SignalProvideClarification = "goaai.runtime.provide.clarification"

	// SignalProvideToolResults delivers external tool results to a waiting run.
	SignalProvideToolResults = "goaai.runtime.provide.toolresults"

	// SignalProvideConfirmation delivers a ConfirmationDecision to a waiting run.
	SignalProvideConfirmation = "goaai.runtime.provide.confirmation"
)

type (
	// PauseRequest carries metadata attached to a pause signal.
	PauseRequest struct {
		// RunID identifies the run to pause.
		RunID string

		// Reason describes why the run is being paused (for example, "user_requested").
		Reason string

		// RequestedBy identifies the logical actor requesting the pause (for example, a user
		// ID, service name, or "policy_engine").
		RequestedBy string

		// Labels carries optional key-value metadata associated with the pause request.
		Labels map[string]string

		// Metadata carries arbitrary structured data attached to the pause request.
		Metadata map[string]any
	}

	// ResumeRequest carries metadata attached to a resume signal.
	ResumeRequest struct {
		// RunID identifies the run to resume.
		RunID string

		// Notes carries optional human-readable context provided when resuming the run.
		Notes string

		// RequestedBy identifies the logical actor requesting the resume.
		RequestedBy string

		// Labels carries optional key-value metadata associated with the resume request.
		Labels map[string]string

		// Messages allows human or policy actors to inject new conversational messages
		// before the planner resumes execution.
		Messages []*model.Message
	}

	// ClarificationAnswer carries a typed answer for a paused clarification request.
	ClarificationAnswer struct {
		// RunID identifies the run associated with the clarification.
		RunID string

		// ID is the clarification await identifier.
		ID string

		// Answer is the free-form clarification text provided by the actor.
		Answer string

		// Labels carries optional metadata associated with the clarification answer.
		Labels map[string]string
	}

	// ConfirmationDecision carries a typed decision for a confirmation await.
	ConfirmationDecision struct {
		// RunID identifies the run associated with the confirmation.
		RunID string

		// ID is the confirmation await identifier.
		ID string

		// Approved is true when the operator approved the pending action.
		Approved bool

		// RequestedBy identifies the logical actor that provided the decision.
		RequestedBy string

		// Labels carries optional metadata associated with the decision.
		Labels map[string]string

		// Metadata carries arbitrary structured data for audit trails (for example,
		// ticket IDs or justification codes).
		Metadata map[string]any
	}

	// ToolResultsSet carries results for an external tools await request.
	ToolResultsSet struct {
		// RunID identifies the run associated with the external tool results.
		RunID string

		// ID is the await identifier corresponding to the original AwaitExternalTools event.
		ID string

		// Results contains the tool results provided by an external system.
		Results []*planner.ToolResult

		// RetryHints optionally provides hints associated with failures.
		RetryHints []*planner.RetryHint
	}
)
