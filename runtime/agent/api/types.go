// Package api defines shared types that cross workflow/activity boundaries in the
// agent runtime.
package api

import (
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RunInput captures everything an initial or continuation workflow needs.
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
		ToolArgs rawjson.Message

		// Messages carries the conversation history supplied by the caller.
		Messages []*model.Message

		// Labels contains caller-provided metadata (account, priority, etc.).
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

		// Continuation restores a run that previously ended because it required
		// external input. It is nil for the first workflow in a turn chain.
		Continuation *RunContinuationInput
	}

	// RunContinuationInput starts a new workflow from one exact suspended run.
	// The runtime validates Response against the first pending request before it
	// decodes Checkpoint or schedules additional work.
	RunContinuationInput struct {
		// Suspension is the terminal result returned by the preceding workflow.
		Suspension *RunSuspension

		// Response satisfies the first request in Suspension.Pending.
		Response *PendingInputResponse
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

	// TagPolicyClause describes one tag-filtering clause for a run.
	//
	// Contract:
	//   - A tool passes the clause when AllowedAny is empty or intersects the tool tags.
	//   - A tool fails the clause when any DeniedAny tag intersects the tool tags.
	//   - Runtimes apply all configured clauses with logical AND.
	TagPolicyClause struct {
		// AllowedAny allows a tool when at least one listed tag is present. Empty
		// means this clause does not constrain allowed tags.
		AllowedAny []string

		// DeniedAny rejects a tool when any listed tag is present. Empty means
		// this clause does not constrain denied tags.
		DeniedAny []string
	}

	// PolicyOverrides configures per-run policy constraints. All fields are optional;
	// zero values mean no override.
	PolicyOverrides struct {
		// RestrictToTool restricts tool execution to the given tool identifier.
		RestrictToTool tools.Ident

		// CompletionTool identifies the budgeted tool whose first successful
		// execution completes this run without a final planner response. The run
		// fails rather than synthesizing a substitute response if it terminates
		// before this tool succeeds.
		CompletionTool tools.Ident

		// TagClauses applies explicit tag-policy clauses using logical AND.
		TagClauses []TagPolicyClause

		// MaxToolCalls caps the total number of budgeted (non-bookkeeping) tool
		// calls a run may execute.
		MaxToolCalls int

		// MaxConsecutiveFailedToolCalls caps the number of consecutive failing
		// tool batches before finalizing: a batch whose budgeted calls all fail
		// consumes one unit, and any budgeted success resets the streak.
		MaxConsecutiveFailedToolCalls int

		// TimeBudget caps active planner and tool work. External-input waits do
		// not consume this budget.
		TimeBudget time.Duration

		// PlanTimeout overrides the per-turn plan/resume activity timeout.
		PlanTimeout time.Duration

		// ToolTimeout overrides the default per-tool execution timeout.
		ToolTimeout time.Duration

		// PerToolTimeout overrides tool execution timeouts for specific tools.
		PerToolTimeout map[tools.Ident]time.Duration

		// FinalizerGrace extends the active TimeBudget deadline into the Hard
		// deadline available to finalization and bookkeeping.
		FinalizerGrace time.Duration

		// LimitTerminalPlans supplies one terminal tool call for each way a run
		// can exhaust its configured limits. When nil, the planner writes the
		// final response from saved messages.
		LimitTerminalPlans *LimitTerminalPlans
	}

	// LimitTerminalPlans contains the complete set of calls a workflow may
	// select after normal work reaches a configured limit.
	LimitTerminalPlans struct {
		// TimeBudget runs when active planner and tool work exhausts TimeBudget.
		TimeBudget LimitTerminalCall

		// ToolCallCap runs when budgeted tool calls exhaust MaxToolCalls.
		ToolCallCap LimitTerminalCall

		// FailedToolCallCap runs when consecutive failed tool batches exhaust
		// MaxConsecutiveFailedToolCalls.
		FailedToolCallCap LimitTerminalCall
	}

	// LimitTerminalCall contains only the application-selected terminal tool
	// and its validated JSON payload. Goa-AI adds run identifiers, labels, the
	// limit reason, and a tool-call identifier when it executes the call.
	LimitTerminalCall struct {
		// Name identifies a registered terminal bookkeeping tool.
		Name tools.Ident

		// Payload is JSON accepted by the tool's generated payload codec.
		Payload rawjson.Message
	}

	// RunOutput represents the terminal outcome returned by one workflow,
	// including either a completed result or a suspension plus accumulated tool
	// traces and planner notes for callers.
	RunOutput struct {
		// AgentID echoes the agent that produced the result.
		AgentID agent.Ident

		// RunID echoes the workflow execution identifier.
		RunID string

		// Final is the assistant reply returned to the caller. It is nil when the
		// run ended by terminal-tool contract rather than planner-authored text.
		Final *model.Message

		// FinalToolResult is the canonical parent tool_result for nested agent runs
		// when the child planner/runtime owns the outer tool contract directly.
		//
		// Contract:
		// - This uses the same workflow-safe envelope as ToolEvents because it also
		//   crosses a workflow boundary.
		// - Result bytes are canonical JSON for the parent tool's result schema.
		// - Top-level runs normally leave this nil.
		FinalToolResult *ToolEvent

		// ToolEvents captures all tool results emitted before completion in execution order.
		//
		// Contract:
		// - ToolEvents must be workflow-boundary safe. Do not embed planner.ToolResult here:
		//   planner.ToolResult contains `any` fields (Result) which Temporal will
		//   rehydrate as map[string]any in parent workflows, eliminating strong typing
		//   at the boundary.
		ToolEvents []*ToolEvent

		// Notes aggregates planner annotations produced during the final turn.
		Notes []*planner.PlannerAnnotation

		// Usage aggregates model-reported token usage during the run when available.
		Usage *model.TokenUsage

		// Suspension is set instead of Final when the run ended because it needs
		// external input. The caller continues by starting a new workflow with this
		// value and one matching PendingInputResponse.
		Suspension *RunSuspension
	}

	// RunSuspension is the complete workflow-safe result of stopping for external
	// input. Applications must keep the complete value in trusted server-side
	// storage; Checkpoint may contain private transcript and execution state and
	// may only be decoded by goa-ai.
	RunSuspension struct {
		// ID uniquely identifies the exact checkpoint and visible pending requests.
		ID string

		// Version identifies the checkpoint schema understood by the runtime.
		Version string

		// Checkpoint contains canonical JSON owned exclusively by the runtime. It
		// must never be sent to an untrusted client.
		Checkpoint rawjson.Message

		// Pending lists required inputs in the exact order they must be supplied.
		Pending []*PendingInput

		// RequiredTools lists the registered tools referenced by the saved state.
		// A continuation worker must register every name; its generated codecs then
		// validate the concrete saved payloads and results during restoration.
		RequiredTools []tools.Ident
	}

	// PendingInputKind identifies the one response shape accepted for a pending
	// external-input request.
	PendingInputKind string

	// PendingInput describes one exact external input requested by the runtime.
	// Exactly one payload field must be set and must match Kind.
	PendingInput struct {
		// Kind selects the required response shape.
		Kind PendingInputKind

		// Confirmation is set for a tool authorization decision.
		Confirmation *PendingConfirmation

		// Await is set for planner-authored clarification, questions, or external tools.
		Await *planner.AwaitItem
	}

	// PendingConfirmation describes one tool call that cannot execute until a
	// person explicitly approves or denies it.
	PendingConfirmation struct {
		// ID uniquely identifies this request.
		ID string

		// Title is the short heading displayed with the decision.
		Title string

		// Prompt explains the decision being requested.
		Prompt string

		// ToolName is the exact tool awaiting authorization.
		ToolName tools.Ident

		// ToolCallID is the model-authored correlation identifier.
		ToolCallID string

		// Payload is the exact canonical tool input awaiting authorization.
		Payload rawjson.Message
	}

	// PendingInputResponse supplies exactly one response to the first pending
	// request in a RunSuspension. Exactly one field must be set.
	PendingInputResponse struct {
		// Clarification supplies free-form user text.
		Clarification *ClarificationAnswer

		// Confirmation supplies an approval or denial decision.
		Confirmation *ConfirmationDecision

		// ToolResults supplies structured question answers or external tool results.
		ToolResults *ToolResultsSet
	}

	// ToolEvent is the workflow-boundary safe representation of a tool result emitted by a run.
	//
	// Contract:
	// - Result and ServerData are canonical JSON bytes, not decoded Go values.
	// - Runtimes must decode Result bytes using the registered tool result codec.
	// - This is required for agent-as-tool: child workflow outputs cross a workflow boundary,
	//   and `any` fields would otherwise rehydrate as map[string]any.
	ToolEvent struct {
		// Name is the fully-qualified tool identifier that produced this result.
		Name tools.Ident

		// Result is the canonical JSON result payload encoded using the tool result codec.
		Result rawjson.Message

		// ResultBytes is the size, in bytes, of the canonical JSON result payload
		// produced by the runtime before any workflow-boundary trimming is applied.
		//
		// When ResultOmitted is true, ResultBytes reports the original size even though
		// Result is nil.
		ResultBytes int

		// ResultOmitted indicates that the runtime intentionally omitted Result bytes
		// from this envelope to satisfy workflow-boundary payload budgets.
		//
		// This is used for workflow-safe child/final tool-result envelopes: workflow
		// orchestration must not shuttle arbitrarily large result payloads. Full tool
		// results remain available via the canonical run log when that path owns the
		// execution history.
		ResultOmitted bool

		// ResultOmittedReason provides a stable, machine-readable reason for omitting
		// the result bytes. Empty when ResultOmitted is false.
		//
		// Example values: "workflow_budget".
		ResultOmittedReason string

		// ServerData carries server-only data emitted alongside the tool result.
		// It is never sent to model providers.
		ServerData rawjson.Message

		// Bounds, when non-nil, describes how the result has been bounded relative
		// to the full underlying data set (for example, list/window/graph caps).
		Bounds *agent.Bounds

		// Failure is the structured tool failure when execution did not produce a
		// result.
		Failure *planner.ToolFailure

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

	// ToolOutputRef is the workflow-boundary safe reference to one canonical tool
	// output stored in the run log.
	//
	// Contract:
	// - This is a pure identity envelope for planner resume/finalization.
	// - The runtime hydrates all planner-visible tool state from canonical run-log
	//   events before invoking planners.
	ToolOutputRef struct {
		// CallRunID identifies the run log containing ToolCallScheduled.
		CallRunID string

		// ResultRunID identifies the run log containing ToolResultReceived.
		ResultRunID string

		// ToolCallID is the correlation identifier for this tool invocation.
		ToolCallID string
	}

	// RecoveryCatalog records the exact tools advertised by a recovery planner
	// activity. Its presence lets the workflow enforce the same catalog after
	// the activity returns; absence preserves replay of histories recorded
	// before recovery catalogs were introduced.
	RecoveryCatalog struct {
		// Tools lists canonical tool identifiers in advertised order. An empty
		// list means the recovery turn required a tool-free result.
		Tools []tools.Ident
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

		// Policy snapshots the per-run policy visible to the planner activity.
		// Runtimes use this to shape the planner-visible advertised tool list.
		Policy *PolicyOverrides

		// ToolOutputs is the accumulated executed tool-call history for the run so far.
		//
		// Contract:
		// - This is the sole planner-facing execution-history field across the
		//   workflow/activity boundary.
		// - Entries are references only; they do not inline planner-visible tool data.
		// - The runtime rehydrates canonical tool input, result, server data, and
		//   planner-visible metadata from the run event log before invoking planners.
		ToolOutputs []*ToolOutputRef

		// RecoveryToolCallIDs selects the failed outputs whose recovery directives
		// shape this planner turn. The activity uses these stable identities to
		// derive its executable catalog and ephemeral recovery guidance from
		// canonical run-log outputs. Omitting the empty field keeps PlanStart and
		// ordinary PlanResume payloads compatible with earlier activity workers.
		RecoveryToolCallIDs []string `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.

		// SynthesisOnly requires the planner to produce a final response without
		// new tool calls. The workflow sets it only when a selected
		// synthesis-after-tools batch has no recoverable failure.
		SynthesisOnly bool

		// Finalize requests a terminal turn with no further domain tool work.
		// The planner may return a final response or terminal bookkeeping calls.
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

		// SessionEnded reports that the run's durable session was ended before
		// this turn could be planned: the activity refused to plan and Result
		// is nil. The workflow terminates the run as canceled. This is the
		// turn-boundary enforcement of session lifecycle — the durable session
		// status is the authority; engine cancellation only expedites
		// shutdown.
		SessionEnded bool

		// RecoveryCatalog is present only for a recovery-aware resume activity
		// and records the exact executable catalog shown to that planner turn.
		RecoveryCatalog *RecoveryCatalog `json:",omitempty"` //nolint:tagliatelle // Temporal payloads retain Go field names.
	}

	// RecordActivityInput is the canonical workflow-to-activity envelope for
	// durable runtime records.
	RecordActivityInput = runlog.ActivityInput

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
		Payload rawjson.Message

		// SessionID is the logical session identifier (for example, a chat conversation).
		SessionID string

		// Labels carries caller-defined run metadata dimensions used by runtime
		// policies and prompt scoping.
		Labels map[string]string

		// TurnID identifies the conversational turn that produced this tool call.
		TurnID string

		// ParentToolCallID is the identifier of the parent tool call when this invocation is nested.
		ParentToolCallID string
	}

	// ToolOutput is returned by tool executors after invoking the tool implementation.
	ToolOutput struct {
		// Payload is the tool result encoded as JSON. The runtime decodes it using the registered tool codec.
		Payload rawjson.Message

		// ServerData carries server-only data emitted alongside the tool result. It is
		// never forwarded to model providers, but it must cross workflow/activity
		// boundaries so in-process subscribers (persistence, metrics, UIs) can
		// consume it.
		//
		// Contract:
		//   - This is canonical JSON (typically a JSON array of server data items).
		//   - Tool implementations may return nil when no server data is present.
		ServerData rawjson.Message

		// Bounds carries canonical bounded-result metadata across workflow/activity
		// boundaries when the tool contract declares an out-of-band bounds channel.
		Bounds *agent.Bounds

		// Telemetry contains execution timing and provider usage metadata when available.
		Telemetry *telemetry.ToolTelemetry

		// Failure is the structured tool failure when execution did not produce a
		// result.
		Failure *planner.ToolFailure

		// Clarification carries an optional user question emitted with this tool
		// result.
		//
		// Contract:
		//   - This survives the tool activity boundary so the workflow can end with
		//     a continuation request before the next planner call.
		//   - The runtime consumes it from the current batch only and does not
		//     persist it into cumulative planner ToolOutputs history.
		Clarification *ToolClarification
	}

	// ToolClarification requests free-form user input after a tool result and
	// before the runtime's next planner call.
	ToolClarification struct {
		// ID uniquely identifies this clarification request.
		ID string

		// Question is the operator-facing prompt to present.
		Question string
	}

	// ClarificationAnswer carries a typed answer for a suspended clarification request.
	ClarificationAnswer struct {
		// ID is the clarification await identifier.
		ID string

		// Answer is the free-form clarification text provided by the actor.
		Answer string

		// Labels carries optional metadata associated with the clarification answer.
		Labels map[string]string
	}

	// ConfirmationDecision carries a typed decision for a confirmation await.
	ConfirmationDecision struct {
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

	// ProvidedToolResult carries one externally supplied tool result into a
	// continuation workflow.
	//
	// Contract:
	// - This is a raw input envelope from an external actor (for example, a
	//   user-interface service), not the runtime's canonical `ToolEvent` representation.
	// - Result bytes must use the tool's generated result codec and remain
	//   canonical JSON, but server-only sidecars are never provided here; the
	//   runtime materializes them after decoding.
	ProvidedToolResult struct {
		// Name is the fully-qualified tool identifier that produced this result.
		Name tools.Ident

		// ToolCallID correlates the result with the original awaited tool call.
		ToolCallID string

		// Success is set exactly when external execution produced a result.
		Success *ProvidedToolSuccess

		// Failure is the externally observed failure when execution did not
		// produce a result. Exactly one of Success and Failure must be set.
		Failure *ProvidedToolFailure
	}

	// ProvidedToolSuccess carries a successful external result and its optional
	// bounded-result metadata.
	ProvidedToolSuccess struct {
		// Result contains canonical JSON for the tool's result contract. JSON
		// null remains a successful result when the registered codec permits it.
		Result rawjson.Message

		// Bounds carries bounded-result metadata when the tool contract requires it.
		Bounds *agent.Bounds
	}

	// ProvidedToolFailure carries the failure facts owned by an external tool
	// executor. The runtime combines these facts with the awaited call and
	// registered tool metadata to construct a canonical planner.ToolFailure.
	ProvidedToolFailure struct {
		// Kind classifies why the external execution failed.
		Kind planner.FailureKind

		// Message describes the external execution failure.
		Message string

		// Action declares the legal next planner transition.
		Action planner.RecoveryAction

		// Issues contains generated field issues when the external boundary
		// rejected model-authored input.
		Issues []*tools.FieldIssue
	}

	// ToolResultsSet carries results for an external-tools or structured-question
	// input request.
	ToolResultsSet struct {
		// ID is the await identifier corresponding to the original AwaitExternalTools event.
		ID string

		// Results contains the tool results provided by an external system.
		//
		// Contract:
		// - This field crosses a workflow continuation boundary. It must be wire-safe and must
		//   not embed planner.ToolResult (which contains `any`) or the runtime-owned
		//   `ToolEvent` envelope.
		// - The runtime owns decoding, result materialization, canonicalization,
		//   and server-side sidecar attachment after the continuation is received.
		Results []*ProvidedToolResult
	}
)

const (
	// PendingInputKindClarification requires a Clarification response.
	PendingInputKindClarification PendingInputKind = "clarification"

	// PendingInputKindConfirmation requires a Confirmation response.
	PendingInputKindConfirmation PendingInputKind = "confirmation"

	// PendingInputKindToolResults requires a ToolResults response.
	PendingInputKindToolResults PendingInputKind = "tool_results"

	// RunSuspensionVersion is the checkpoint schema emitted by this runtime.
	RunSuspensionVersion = "goa-ai.run-suspension.v2"
)
