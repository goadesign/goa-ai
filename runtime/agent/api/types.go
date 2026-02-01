// Package api defines shared types that cross workflow/activity boundaries in the
// agent runtime.
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

type (
	// RunInput captures everything a workflow needs to start or resume a run.
	// It includes the full conversational context plus caller-provided labels and
	// metadata.
	RunInput struct {
		// AgentID identifies which agent should process the run.
		AgentID agent.Ident

		// RunID is the durable workflow execution identifier.
		RunID string

		// SessionID groups related runs (for example, multi-turn conversations).
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

	// WorkflowOptions mirrors a subset of engine start options exposed through the runtime.
	// Engine adapters convert these into native options at start time.
	WorkflowOptions struct {
		// Memo is a map of key-value pairs that can be used to store data for the workflow.
		Memo map[string]any

		// SearchAttributes is a map of key-value pairs indexed by the engine for visibility.
		SearchAttributes map[string]any

		// TaskQueue is the name of the task queue to use for the workflow.
		TaskQueue string

		// RetryPolicy is the retry policy to use for workflow start.
		RetryPolicy RetryPolicy
	}

	// RetryPolicy defines retry semantics shared by workflows and activities at the API layer.
	RetryPolicy struct {
		// MaxAttempts caps the total number of retry attempts. Zero means engine default.
		MaxAttempts int

		// InitialInterval is the delay before the first retry. Zero means engine default.
		InitialInterval time.Duration

		// BackoffCoefficient multiplies the delay after each retry (for example, 2.0 for exponential).
		BackoffCoefficient float64
	}

	// PolicyOverrides configures per-run policy constraints. All fields are optional;
	// zero values mean no override.
	PolicyOverrides struct {
		// PerTurnMaxToolCalls limits the number of tool calls the planner may issue per turn.
		PerTurnMaxToolCalls int

		// RestrictToTool restricts tool execution to the given tool identifier.
		RestrictToTool tools.Ident

		// AllowedTags restricts tool execution to tools tagged with at least one of the listed tags.
		AllowedTags []string

		// DeniedTags excludes tools tagged with any of the listed tags from execution.
		DeniedTags []string

		// MaxToolCalls caps the total number of tool calls a run may execute.
		MaxToolCalls int

		// MaxConsecutiveFailedToolCalls caps the number of consecutive failing tool calls before finalizing.
		MaxConsecutiveFailedToolCalls int

		// TimeBudget caps the total wall-clock runtime budget for the run.
		TimeBudget time.Duration

		// PlanTimeout overrides the per-turn plan/resume activity timeout.
		PlanTimeout time.Duration

		// ToolTimeout overrides the default per-tool execution timeout.
		ToolTimeout time.Duration

		// PerToolTimeout overrides tool execution timeouts for specific tools.
		PerToolTimeout map[tools.Ident]time.Duration

		// FinalizerGrace caps the time spent in the finalizer phase after termination is requested.
		FinalizerGrace time.Duration

		// InterruptsAllowed enables interrupt/pause behavior for the run when supported by the engine.
		InterruptsAllowed bool
	}

	// RunOutput represents the final outcome returned by a run workflow, including the
	// concluding assistant message plus tool traces and planner notes for callers.
	RunOutput struct {
		// AgentID echoes the agent that produced the result.
		AgentID agent.Ident

		// RunID echoes the workflow execution identifier.
		RunID string

		// Final is the assistant reply returned to the caller.
		Final *model.Message

		// ToolEvents captures all tool results emitted before completion in execution order.
		//
		// Contract:
		// - ToolEvents must be workflow-boundary safe. Do not embed planner.ToolResult here:
		//   planner.ToolResult contains `any` fields (Result, Artifact.Data) which Temporal will
		//   rehydrate as map[string]any in parent workflows, breaking sidecar codecs and
		//   eliminating strong typing at the boundary.
		ToolEvents []*ToolEvent

		// Notes aggregates planner annotations produced during the final turn.
		Notes []*planner.PlannerAnnotation

		// Usage aggregates model-reported token usage during the run when available.
		Usage *model.TokenUsage
	}

	// ToolEvent is the workflow-boundary safe representation of a tool result emitted by a run.
	//
	// Contract:
	// - Result and Artifacts.Data are canonical JSON bytes, not decoded Go values.
	// - Runtimes must decode these bytes using the registered tool codec/sidecar codec.
	// - This is required for agent-as-tool: child workflow outputs cross a workflow boundary,
	//   and `any` fields would otherwise rehydrate as map[string]any.
	ToolEvent struct {
		// Name is the fully-qualified tool identifier that produced this result.
		Name tools.Ident

		// Result is the canonical JSON result payload encoded using the tool result codec.
		Result json.RawMessage

		// ResultBytes is the size, in bytes, of the canonical JSON result payload
		// produced by the runtime before any workflow-boundary trimming is applied.
		//
		// When ResultOmitted is true, ResultBytes reports the original size even though
		// Result is nil.
		ResultBytes int

		// ResultOmitted indicates that the runtime intentionally omitted Result bytes
		// from this envelope to satisfy workflow-boundary payload budgets.
		//
		// This is used in planner activity inputs: workflow orchestration must not
		// shuttle large tool payloads. Full tool results remain available via hooks
		// (memory/streams) and in RunOutput.ToolEvents.
		ResultOmitted bool

		// ResultOmittedReason provides a stable, machine-readable reason for omitting
		// the result bytes. Empty when ResultOmitted is false.
		//
		// Example values: "workflow_budget".
		ResultOmittedReason string

		// Artifacts contains sideband UI artifacts produced alongside the tool result.
		Artifacts []*ToolArtifact

		// Bounds, when non-nil, describes how the result has been bounded relative
		// to the full underlying data set (for example, list/window/graph caps).
		Bounds *agent.Bounds

		// Error is the structured tool error when tool execution failed.
		Error *planner.ToolError

		// RetryHint is optional structured guidance for recovering from tool failures.
		RetryHint *planner.RetryHint

		// Telemetry contains tool execution metrics (duration, token usage, model).
		Telemetry *telemetry.ToolTelemetry

		// ToolCallID is the correlation identifier for this tool invocation.
		ToolCallID string

		// ChildrenCount records how many nested tool results were observed when this
		// result came from an agent-as-tool execution.
		ChildrenCount int

		// RunLink links this tool result to a nested agent run when it was produced by
		// an agent-as-tool. Nil for service-backed tools.
		RunLink *run.Handle
	}

	// PlanActivityInput carries the planner input for PlanStart and PlanResume activities.
	PlanActivityInput struct {
		// AgentID identifies which agent is being planned.
		AgentID agent.Ident

		// RunID identifies the run being planned.
		RunID string

		// Messages is the current conversation transcript provided to the planner.
		Messages []*model.Message

		// RunContext carries nested-run metadata (parent IDs, tool identifiers, etc.).
		RunContext run.Context

		// ToolResults are the tool results produced by the previous turn (if any).
		//
		// Contract:
		// - This field crosses the workflow/activity boundary. It must not embed
		//   planner.ToolResult because planner.ToolResult contains `any` fields that
		//   engine data converters may rehydrate as map[string]any, breaking tool
		//   result and sidecar codecs.
		// - Results must be encoded with the tool's generated result codec and stored
		//   as canonical JSON bytes.
		ToolResults []*ToolEvent

		// Finalize requests a final turn with no further tool calls.
		Finalize *planner.Termination
	}

	// PlanActivityOutput wraps the planner result produced by a plan/resume activity.
	PlanActivityOutput struct {
		// Result is the planner output describing next tool calls, await requests, or final response.
		Result *planner.PlanResult

		// Transcript contains the provider-visible transcript produced by the planner.
		Transcript []*model.Message

		// Usage is the token usage reported by the model provider when available.
		Usage model.TokenUsage
	}

	// HookActivityInput is the canonical workflow-to-activity envelope for hook events.
	HookActivityInput = hooks.ActivityInput

	// ToolInput is the payload passed to tool executors. Payload is JSON-encoded.
	ToolInput struct {
		// RunID identifies the run that owns this tool call.
		RunID string

		// AgentID identifies the agent that owns this tool call.
		AgentID agent.Ident

		// ToolsetName identifies the owning toolset when known; it may be empty when inferred by ToolName.
		ToolsetName string

		// ToolName is the fully-qualified tool identifier.
		ToolName tools.Ident

		// ToolCallID uniquely identifies the tool invocation for correlation across events.
		ToolCallID string

		// Payload is the canonical JSON payload for the tool call.
		Payload json.RawMessage

		// ArtifactsMode is the normalized per-call artifacts toggle selected by the caller via the reserved
		// `artifacts` payload field. When empty, the caller did not specify a mode.
		ArtifactsMode tools.ArtifactsMode

		// SessionID is the logical session identifier (for example, a chat conversation).
		SessionID string

		// TurnID identifies the conversational turn that produced this tool call.
		TurnID string

		// ParentToolCallID is the identifier of the parent tool call when this invocation is nested.
		ParentToolCallID string
	}

	// ToolOutput is returned by tool executors after invoking the tool implementation.
	ToolOutput struct {
		// Payload is the tool result encoded as JSON. The runtime decodes it using the registered tool codec.
		Payload json.RawMessage

		// Artifacts contains sideband UI artifacts produced alongside the tool result.
		//
		// Boundary contract:
		// This field crosses workflow/activity boundaries. It must not contain `any`
		// payloads because engine data converters will rehydrate interface values as
		// map/slice types, losing the tool's compiled sidecar schema.
		//
		// Providers must encode artifacts using the tool sidecar codec and store the
		// canonical JSON bytes here.
		Artifacts []*ToolArtifact

		// Telemetry contains execution timing and provider usage metadata when available.
		Telemetry *telemetry.ToolTelemetry

		// Error is a plain-text error message when tool execution failed.
		Error string

		// RetryHint provides structured retry guidance when execution failed due to invalid payloads.
		RetryHint *planner.RetryHint
	}

	// ToolArtifact is a tool-produced artifact payload that crosses workflow/activity boundaries.
	//
	// Data is the canonical JSON encoding produced by the tool's sidecar codec.
	// Consumers may decode it using the sidecar codec for the originating tool.
	ToolArtifact struct {
		// Kind identifies the logical artifact shape (e.g., "atlas.topology").
		Kind string `json:"kind"`
		// Data contains the artifact payload as canonical JSON bytes.
		Data json.RawMessage `json:"data"`
		// SourceTool is the fully-qualified tool identifier that produced this artifact.
		SourceTool tools.Ident `json:"source_tool"`
		// RunLink links this artifact to a nested agent run when it was produced by an
		// agent-as-tool. Nil for service-backed tools.
		RunLink *run.Handle `json:"run_link,omitempty"`
	}

	// PauseRequest carries metadata attached to a pause signal.
	PauseRequest struct {
		// RunID identifies the run to pause.
		RunID string

		// Reason describes why the run is being paused (for example, "user_requested").
		Reason string

		// RequestedBy identifies the logical actor requesting the pause (for example, a user ID, service name,
		// or "policy_engine").
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

		// Messages allows human or policy actors to inject new conversational messages before the planner resumes execution.
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

		// Metadata carries arbitrary structured data for audit trails (for example, ticket IDs or justification codes).
		Metadata map[string]any
	}

	// ToolResultsSet carries results for an external tools await request.
	ToolResultsSet struct {
		// RunID identifies the run associated with the external tool results.
		RunID string

		// ID is the await identifier corresponding to the original AwaitExternalTools event.
		ID string

		// Results contains the tool results provided by an external system.
		//
		// Contract:
		// - This field crosses a workflow signal boundary. It must be wire-safe and must
		//   not embed planner.ToolResult (which contains `any`).
		// - Results must be encoded with the tool's generated result codec and stored
		//   as canonical JSON bytes. Artifacts (if any) must be encoded with the tool's
		//   sidecar codec.
		Results []*ToolEvent

		// RetryHints optionally provides hints associated with failures.
		RetryHints []*planner.RetryHint
	}
)

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

// UnmarshalJSON handles decoding PlanActivityOutput so that Transcript entries are
// deserialized through the richer model.Message decoder (which materializes Part
// implementations). This keeps workflows resilient to legacy payloads.
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
