package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/toolerrors"
	"goa.design/goa-ai/runtime/agent/tools"

	"go.temporal.io/sdk/temporal"
)

type (
	// Event is the interface all hook events must implement. The runtime publishes
	// events through the Bus, and subscribers receive them via HandleEvent.
	// Concrete event types carry typed payloads for each lifecycle phase.
	//
	// Subscribers use type switches to access event-specific fields:
	//
	//	func (s *MySubscriber) HandleEvent(ctx context.Context, evt Event) error {
	//	    switch e := evt.(type) {
	//	    case *WorkflowStartedEvent:
	//	        log.Printf("Context: %+v", e.RunContext)
	//	    case *ToolResultReceivedEvent:
	//	        log.Printf("Tool %s took %v", e.ToolName, e.Duration)
	//	    }
	//	    return nil
	//	}
	Event interface {
		// Type returns the specific event type constant (e.g., RunStarted, ToolCallScheduled).
		// Subscribers use this to filter events or route to specific handlers without
		// type assertions.
		Type() EventType
		// RunID returns the unique identifier for the workflow run that produced this event.
		// All events within a single run execution share the same run ID. This allows
		// correlation across distributed systems and enables filtering events by run.
		RunID() string
		// SessionID returns the logical session identifier associated with the run.
		// All events for a given run share the same session ID, providing a stable
		// join key across processes and transports.
		SessionID() string
		// AgentID returns the agent identifier that triggered this event. Subscribers can
		// use this to filter events by agent when multiple agents run in the same system.
		AgentID() string
		// Timestamp returns the Unix timestamp in milliseconds when the event occurred.
		// Events are timestamped at creation, not at delivery, so subscribers can calculate
		// durations and latencies between related events.
		Timestamp() int64
		// TurnID returns the conversational turn identifier if turn tracking is active,
		// empty string otherwise. A turn groups events for a single user interaction cycle
		// (e.g., from user message through final assistant response). UI systems use this
		// to render threaded conversations.
		TurnID() string
	}

	// RunStartedEvent fires when a run begins execution.
	RunStartedEvent struct {
		baseEvent
		// RunContext carries the execution metadata (run ID, attempt, labels, caps)
		// for this run invocation.
		RunContext run.Context
		// Input is the initial payload passed to the run, typically containing
		// messages and caller-provided metadata.
		Input any
	}

	// RunCompletedEvent fires after a run finishes, whether
	// successfully or with a failure.
	RunCompletedEvent struct {
		baseEvent
		// Status indicates the final outcome: "success", "failed", or "canceled".
		Status string
		// PublicError is a user-safe, deterministic summary of the terminal failure.
		// It is empty on success and cancellations. On failures, it is populated
		// and is intended to be rendered directly in UIs without additional parsing.
		PublicError string
		// Error contains any terminal error that halted the run. Nil on success.
		Error error
		// ErrorProvider identifies the model provider when the terminal error was
		// caused by a provider failure (for example, "bedrock").
		ErrorProvider string
		// ErrorOperation identifies the provider operation when available.
		ErrorOperation string
		// ErrorKind classifies provider failures into a small set of stable categories
		// suitable for retry and UX decisions (for example, "auth" or "invalid_request").
		ErrorKind string
		// ErrorCode is the provider-specific error code when available.
		ErrorCode string
		// HTTPStatus is the provider HTTP status code when available.
		HTTPStatus int
		// Retryable reports whether retrying may succeed without changing the request.
		Retryable bool
		// Phase captures the terminal phase for the run. For successful runs this
		// is typically PhaseCompleted; failures map to PhaseFailed; cancellations
		// map to PhaseCanceled.
		Phase run.Phase
	}

	// RunPausedEvent fires when a run is intentionally paused.
	RunPausedEvent struct {
		baseEvent
		// Reason provides a human-readable explanation for why the run was paused.
		// Examples: "user_requested", "approval_required", "manual_review_needed".
		// Subscribers can use this to categorize pause events and display appropriate
		// messages to end users.
		Reason string
		// RequestedBy identifies the actor who initiated the pause (e.g., user ID,
		// service name, or "policy_engine"). This enables audit logging and attribution
		// for governance workflows.
		RequestedBy string
		// Labels carries optional key-value metadata for categorizing the pause event.
		// These labels are propagated from the pause request and can be used for filtering,
		// reporting, or triggering downstream workflows. Nil if no labels were provided.
		Labels map[string]string
		// Metadata holds arbitrary structured data attached to the pause request for audit
		// trails or workflow-specific logic (e.g., approval ticket IDs, escalation reasons).
		// The runtime persists this alongside the run status. Nil if no metadata was provided.
		Metadata map[string]any
	}

	// RunResumedEvent fires when a paused run resumes.
	RunResumedEvent struct {
		baseEvent
		// Notes carries optional human-readable context provided when resuming the run.
		// This might include instructions for the planner ("focus on X"), approval
		// summaries, or other guidance. Empty if no notes were provided with the resume request.
		Notes string
		// RequestedBy identifies the actor who initiated the resume (e.g., user ID,
		// service name, or "approval_system"). This enables audit logging and attribution
		// for governance workflows.
		RequestedBy string
		// Labels carries optional key-value metadata for categorizing the resume event.
		// These labels are propagated from the resume request and can be used for filtering,
		// reporting, or triggering downstream workflows. Nil if no labels were provided.
		Labels map[string]string
		// MessageCount indicates how many new conversational messages were injected when
		// resuming the run. When greater than zero, these messages are appended to the
		// planner's context before execution continues. Subscribers can use this to track
		// human-in-the-loop interventions.
		MessageCount int
	}

	// AgentRunStartedEvent fires in the parent run when an agent-as-tool
	// child run is started. It links the parent tool call to the child
	// agent run for streaming and observability.
	AgentRunStartedEvent struct {
		baseEvent
		// ToolName is the canonical tool identifier for the parent tool.
		ToolName tools.Ident
		// ToolCallID is the parent tool call identifier.
		ToolCallID string
		// ChildRunID is the run identifier of the nested agent execution.
		ChildRunID string
		// ChildAgentID is the identifier of the nested agent.
		ChildAgentID agent.Ident
	}

	// RunPhaseChangedEvent fires when a run transitions between lifecycle phases
	// (prompted, planning, executing_tools, synthesizing, completed, failed,
	// canceled). This is a higher-fidelity signal than Status and is primarily
	// intended for streaming/UX consumers.
	RunPhaseChangedEvent struct {
		baseEvent
		// Phase is the new lifecycle phase for the run.
		Phase run.Phase
	}

	// ToolCallScheduledEvent fires when the runtime schedules a tool activity
	// for execution.
	ToolCallScheduledEvent struct {
		baseEvent
		// ToolCallID uniquely identifies the scheduled tool invocation so progress
		// updates can correlate with the original request.
		ToolCallID string
		// ToolName is the globally unique tool identifier (simple DSL name).
		ToolName tools.Ident
		// Payload contains the canonical JSON tool arguments for the scheduled tool.
		// It is a json.RawMessage representing the tool payload object as seen by the
		// runtime and codecs.
		Payload json.RawMessage
		// Queue is the activity queue name where the tool execution is scheduled.
		Queue string
		// ParentToolCallID optionally identifies the tool call that requested this tool.
		// Empty for top-level planner-requested tools. Used to track parent-child chains.
		ParentToolCallID string
		// ExpectedChildrenTotal indicates how many child tools are expected from this batch.
		// A value of 0 means no children expected or count not tracked by the planner.
		ExpectedChildrenTotal int
		// DisplayHint is a human-facing summary of the in-flight tool work derived
		// from the tool's call hint template. It is computed once by the runtime
		// so downstream subscribers (streaming, session persistence, memory) can
		// surface consistent labels without re-rendering templates.
		DisplayHint string
	}

	// ToolResultReceivedEvent fires when a tool activity completes and returns
	// a result or error.
	ToolResultReceivedEvent struct {
		baseEvent
		// ToolCallID uniquely identifies the tool invocation that produced this result.
		ToolCallID string
		// ParentToolCallID identifies the parent tool call if this tool was invoked by another tool.
		// Empty when the tool call was scheduled directly by the planner.
		ParentToolCallID string
		// ToolName is the globally unique tool identifier that was executed.
		ToolName tools.Ident
		// Result contains the tool's output payload. Nil if Error is set.
		Result any
		// ResultPreview is a concise, user-facing summary of the tool result rendered
		// from the registered ResultHintTemplate for this tool. It is computed by
		// the runtime while the result is still strongly typed so downstream
		// subscribers do not need to re-render templates from JSON-decoded maps.
		ResultPreview string
		// Bounds, when non-nil, describes how the tool result has been bounded
		// relative to the full underlying data set. It is supplied by tool
		// implementations and surfaced for observability; the runtime does not
		// modify it.
		Bounds *agent.Bounds
		// Artifacts holds rich, non-provider data attached to the tool result.
		// Artifacts are never serialized into model provider requests.
		Artifacts []*planner.Artifact
		// Duration is the wall-clock execution time for the tool activity.
		Duration time.Duration
		// Telemetry holds structured observability metadata (tokens, model, retries).
		// Nil if no telemetry was collected.
		Telemetry *telemetry.ToolTelemetry
		// Error contains any error returned by the tool execution. Nil on success.
		Error *toolerrors.ToolError
	}

	// ToolCallUpdatedEvent fires when a tool call's metadata is updated after
	// initial scheduling. This typically occurs when a parent tool (agent-as-tool)
	// dynamically discovers additional child tools across multiple planning iterations.
	// UIs use this to update progress displays ("3 of 5 children complete").
	ToolCallUpdatedEvent struct {
		baseEvent
		// ToolCallID identifies the tool call being updated (usually a parent call).
		ToolCallID string
		// ExpectedChildrenTotal is the new count of expected child tools. This value
		// grows as child tools are discovered dynamically during execution.
		ExpectedChildrenTotal int
	}

	// PlannerNoteEvent fires when the planner emits an annotation or
	// intermediate thought during execution.
	PlannerNoteEvent struct {
		baseEvent
		// Note is the text content of the planner's annotation.
		Note string
		// Labels provide optional categorization metadata (e.g., "type": "reasoning").
		Labels map[string]string
	}

	// ThinkingBlockEvent fires when the planner emits a structured reasoning block
	// (either signed plaintext or redacted bytes). This preserves provider-accurate
	// thinking suitable for exact replay and auditing.
	ThinkingBlockEvent struct {
		baseEvent
		// Text is the plaintext reasoning content when provided by the model.
		Text string
		// Signature is the provider signature for plaintext reasoning (when required).
		Signature string
		// Redacted contains provider-issued redacted reasoning bytes (mutually exclusive with Text).
		Redacted []byte
		// ContentIndex is the provider content block index.
		ContentIndex int
		// Final indicates that the reasoning block was finalized by the provider.
		Final bool
	}

	// AssistantMessageEvent fires when a final assistant response is produced,
	// indicating the workflow is completing with a user-facing message.
	AssistantMessageEvent struct {
		baseEvent
		// Message is the textual content of the assistant's response.
		Message string
		// Structured contains optional typed output (e.g., Pydantic-style structured data).
		// Nil if only a text message is provided.
		Structured any
	}

	// RetryHintIssuedEvent fires when the planner or runtime suggests a retry
	// policy change, such as disabling a failing tool or adjusting caps.
	RetryHintIssuedEvent struct {
		baseEvent
		// Reason summarizes why the retry hint was issued (e.g., "invalid_arguments").
		Reason string
		// ToolName identifies the tool involved in the failure, if applicable.
		ToolName tools.Ident
		// Message provides human-readable guidance for the retry adjustment.
		Message string
	}

	// MemoryAppendedEvent fires when new memory entries are successfully
	// persisted to the memory store.
	MemoryAppendedEvent struct {
		baseEvent
		// EventCount indicates how many memory events were written in this operation.
		EventCount int
	}

	// PolicyDecisionEvent captures the outcome of a policy evaluation so downstream
	// systems can audit allowlists, cap adjustments, and metadata applied for a turn.
	PolicyDecisionEvent struct {
		baseEvent
		// AllowedTools lists the globally unique tool identifiers that the policy engine
		// permitted for this turn. The runtime enforces this allowlist: planners can only
		// invoke tools in this list. An empty slice means no tools are allowed for this turn,
		// forcing the planner to produce a final response. Subscribers use this for security
		// auditing and debugging tool restrictions.
		AllowedTools []tools.Ident
		// Caps reflects the updated execution budgets after policy evaluation for this turn.
		// This includes remaining tool call limits, consecutive failure thresholds, and time
		// budgets. Policies may adjust these dynamically based on observed behavior (e.g.,
		// reducing limits after repeated failures). Subscribers can monitor cap consumption
		// to predict run termination or trigger alerts.
		Caps policy.CapsState
		// Labels carries policy-applied metadata merged into the run context and propagated
		// to subsequent turns. Examples: {"circuit_breaker": "active", "policy_version": "v2"}.
		// These labels appear in downstream telemetry, memory records, and hooks. Subscribers
		// can use them to correlate policy decisions with run outcomes. Nil if the policy did
		// not add labels.
		Labels map[string]string
		// Metadata holds policy-specific structured data for audit trails and compliance
		// reporting (e.g., approval IDs, justification codes, external system responses).
		// The runtime persists this alongside run records. Subscribers can extract this for
		// governance dashboards or regulatory logging. Nil if the policy did not provide metadata.
		Metadata map[string]any
	}

	// baseEvent holds common fields shared by all event types. It is embedded
	// anonymously in each concrete event struct, providing implementations of
	// the RunID, AgentID, Timestamp, and TurnID methods.
	baseEvent struct {
		runID     string
		agentID   agent.Ident
		timestamp int64
		// sessionID associates the event with the logical session that owns the
		// run. All events emitted for a given run share the same session ID.
		sessionID string
		// turnID identifies the conversational turn this event belongs to (optional).
		// When set, groups events for UI rendering and conversation tracking.
		turnID string
	}

	// AwaitClarificationEvent indicates the planner requested a human-provided
	// clarification before continuing execution.
	AwaitClarificationEvent struct {
		baseEvent
		// ID correlates this await with a subsequent ProvideClarification.
		ID string
		// Question is the prompt to present to the user.
		Question string
		// MissingFields optionally lists fields needed to proceed.
		MissingFields []string
		// RestrictToTool optionally narrows the next turn to a specific tool.
		RestrictToTool tools.Ident
		// ExampleInput optionally provides a schema-compliant example.
		ExampleInput map[string]any
	}

	// AwaitConfirmationEvent indicates the runtime requested an explicit operator
	// confirmation before executing a sensitive tool call.
	AwaitConfirmationEvent struct {
		baseEvent
		// ID correlates this await with a subsequent confirmation decision.
		ID string
		// Title is an optional display title for the confirmation UI.
		Title string
		// Prompt is the operator-facing confirmation prompt.
		Prompt string
		// ToolName identifies the tool that requires confirmation.
		ToolName tools.Ident
		// ToolCallID is the tool_call_id for the pending tool call.
		ToolCallID string
		// Payload is the canonical JSON arguments for the pending tool call.
		Payload json.RawMessage
	}

	// ToolAuthorizationEvent indicates an operator provided an explicit approval
	// or denial decision for a pending tool call. This is emitted immediately when
	// the decision is received so subscribers can record a durable audit trail and
	// UIs can render an approval record independent of tool execution.
	ToolAuthorizationEvent struct {
		baseEvent
		// ToolName identifies the tool that was authorized.
		ToolName tools.Ident
		// ToolCallID is the tool_call_id for the pending tool call.
		ToolCallID string
		// Approved reports whether the operator approved execution.
		Approved bool
		// Summary is a deterministic, human-facing description of what was approved.
		Summary string
		// ApprovedBy identifies the actor that provided the decision, formatted as
		// "<principal_type>:<principal_id>".
		ApprovedBy string
	}

	// AwaitExternalToolsEvent indicates the planner requested external tool execution.
	AwaitExternalToolsEvent struct {
		baseEvent
		// ID correlates this await with a subsequent ProvideToolResults.
		ID string
		// Items enumerate the external tool calls to be satisfied.
		Items []AwaitToolItem
	}

	// AwaitToolItem describes a single external tool call to be executed out-of-band.
	AwaitToolItem struct {
		ToolName   tools.Ident
		ToolCallID string
		Payload    json.RawMessage
	}

	// UsageEvent reports token usage for a model invocation within a run.
	// Emitted when the model stream reports usage deltas or a final summary.
	UsageEvent struct {
		baseEvent
		// Model identifier when available (provider dependent).
		Model string
		// InputTokens is the number of prompt tokens consumed.
		InputTokens int
		// OutputTokens is the number of completion tokens produced.
		OutputTokens int
		// TotalTokens is InputTokens + OutputTokens.
		TotalTokens int
		// CacheReadTokens is tokens read from prompt cache (reduced cost).
		CacheReadTokens int
		// CacheWriteTokens is tokens written to prompt cache.
		CacheWriteTokens int
	}

	// HardProtectionEvent signals that the runtime applied a hard protection to
	// avoid a pathological loop or expensive no-op behavior. For example, when
	// an agent-as-tool produced zero child tool calls, the runtime finalizes
	// instead of resuming.
	HardProtectionEvent struct {
		baseEvent
		// Reason is a fixed string describing the protection that was applied.
		// Example: "agent_tool_no_children".
		Reason string
		// ExecutedAgentTools is the number of agent-as-tool executions in the turn.
		ExecutedAgentTools int
		// ChildrenTotal is the total number of child tool calls produced by those
		// agent tools (typically zero when this event fires).
		ChildrenTotal int
		// ToolNames lists the agent-tool identifiers executed in the turn.
		ToolNames []tools.Ident
	}
)

const (
	// ErrorKindTimeout indicates the run failed because a required operation timed out.
	ErrorKindTimeout = "timeout"

	// ErrorKindInternal indicates the run failed for an unclassified reason.
	ErrorKindInternal = "internal"
)

// NewRunStartedEvent constructs a RunStartedEvent with the current
// timestamp. RunContext and Input capture the initial run state.
func NewRunStartedEvent(runID string, agentID agent.Ident, runContext run.Context, input any) *RunStartedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = runContext.SessionID
	return &RunStartedEvent{
		baseEvent:  be,
		RunContext: runContext,
		Input:      input,
	}
}

// NewRunCompletedEvent constructs a RunCompletedEvent. Status should
// be "success", "failed", or "canceled"; phase must be the terminal
// lifecycle phase for the run. err may be nil on success.
func NewRunCompletedEvent(runID string, agentID agent.Ident, sessionID, status string, phase run.Phase, err error) *RunCompletedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	out := &RunCompletedEvent{
		baseEvent: be,
		Status:    status,
		Phase:     phase,
		Error:     err,
	}
	if err == nil {
		return out
	}
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		out.ErrorProvider = pe.Provider()
		out.ErrorOperation = pe.Operation()
		out.ErrorKind = string(pe.Kind())
		out.ErrorCode = pe.Code()
		out.HTTPStatus = pe.HTTPStatus()
		out.Retryable = pe.Retryable()
		if status == "failed" {
			out.PublicError = providerPublicError(pe)
		}
		return out
	}

	// Cancellation is terminal but non-error for UX purposes.
	if status != "failed" {
		return out
	}

	out.ErrorKind, out.PublicError = classifyNonProviderFailure(err)
	out.Retryable = true // Non-provider failures are always retryable.
	return out
}

// NewAgentRunStartedEvent constructs an AgentRunStartedEvent for the given
// parent run, tool, and child run identifiers.
func NewAgentRunStartedEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID, childRunID string, childAgentID agent.Ident) *AgentRunStartedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &AgentRunStartedEvent{
		baseEvent:    be,
		ToolName:     toolName,
		ToolCallID:   toolCallID,
		ChildRunID:   childRunID,
		ChildAgentID: childAgentID,
	}
}

// NewRunPhaseChangedEvent constructs a RunPhaseChangedEvent for the given run
// and agent.
func NewRunPhaseChangedEvent(runID string, agentID agent.Ident, sessionID string, phase run.Phase) *RunPhaseChangedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &RunPhaseChangedEvent{
		baseEvent: be,
		Phase:     phase,
	}
}

// NewRunPausedEvent constructs a RunPausedEvent with provided metadata.
func NewRunPausedEvent(runID string, agentID agent.Ident, sessionID, reason, requestedBy string, labels map[string]string, metadata map[string]any) *RunPausedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &RunPausedEvent{
		baseEvent:   be,
		Reason:      reason,
		RequestedBy: requestedBy,
		Labels:      labels,
		Metadata:    metadata,
	}
}

// NewRunResumedEvent constructs a RunResumedEvent with provided metadata.
func NewRunResumedEvent(runID string, agentID agent.Ident, sessionID, notes, requestedBy string, labels map[string]string, messageCount int) *RunResumedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &RunResumedEvent{
		baseEvent:    be,
		Notes:        notes,
		RequestedBy:  requestedBy,
		Labels:       labels,
		MessageCount: messageCount,
	}
}

// NewAwaitClarificationEvent constructs an AwaitClarificationEvent with the provided details.
func NewAwaitClarificationEvent(runID string, agentID agent.Ident, sessionID, id, question string, missing []string, restrict tools.Ident, example map[string]any) *AwaitClarificationEvent {
	var ex map[string]any
	if len(example) > 0 {
		ex = make(map[string]any, len(example))
		for k, v := range example {
			ex[k] = v
		}
	}
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &AwaitClarificationEvent{
		baseEvent:      be,
		ID:             id,
		Question:       question,
		MissingFields:  append([]string(nil), missing...),
		RestrictToTool: restrict,
		ExampleInput:   ex,
	}
}

// NewAwaitConfirmationEvent constructs an AwaitConfirmationEvent with the provided details.
func NewAwaitConfirmationEvent(runID string, agentID agent.Ident, sessionID, id, title, prompt string, toolName tools.Ident, toolCallID string, payload json.RawMessage) *AwaitConfirmationEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &AwaitConfirmationEvent{
		baseEvent:  be,
		ID:         id,
		Title:      title,
		Prompt:     prompt,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Payload:    payload,
	}
}

// NewToolAuthorizationEvent constructs a ToolAuthorizationEvent for a pending tool call.
func NewToolAuthorizationEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID string, approved bool, summary, approvedBy string) *ToolAuthorizationEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ToolAuthorizationEvent{
		baseEvent:  be,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Approved:   approved,
		Summary:    summary,
		ApprovedBy: approvedBy,
	}
}

// NewAwaitExternalToolsEvent constructs an AwaitExternalToolsEvent.
func NewAwaitExternalToolsEvent(runID string, agentID agent.Ident, sessionID, id string, items []AwaitToolItem) *AwaitExternalToolsEvent {
	// ensure copy
	copied := make([]AwaitToolItem, len(items))
	copy(copied, items)
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &AwaitExternalToolsEvent{
		baseEvent: be,
		ID:        id,
		Items:     copied,
	}
}

// NewPolicyDecisionEvent constructs a PolicyDecisionEvent with the provided metadata.
func NewPolicyDecisionEvent(runID string, agentID agent.Ident, sessionID string, allowed []tools.Ident, caps policy.CapsState, labels map[string]string, metadata map[string]any) *PolicyDecisionEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &PolicyDecisionEvent{
		baseEvent:    be,
		AllowedTools: allowed,
		Caps:         caps,
		Labels:       labels,
		Metadata:     metadata,
	}
}

// Type implements Event for AwaitClarificationEvent.
func (e *AwaitClarificationEvent) Type() EventType { return AwaitClarification }

// Type implements Event for AwaitConfirmationEvent.
func (e *AwaitConfirmationEvent) Type() EventType { return AwaitConfirmation }

// Type implements Event for ToolAuthorizationEvent.
func (e *ToolAuthorizationEvent) Type() EventType { return ToolAuthorization }

// Type implements Event for AwaitExternalToolsEvent.
func (e *AwaitExternalToolsEvent) Type() EventType { return AwaitExternalTools }

// NewToolCallScheduledEvent constructs a ToolCallScheduledEvent. Payload is the
// canonical JSON arguments for the scheduled tool; queue is the activity queue name.
// ParentToolCallID and expectedChildren are optional (empty/0 for top-level calls).
func NewToolCallScheduledEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID string, payload json.RawMessage, queue string, parentToolCallID string, expectedChildren int) *ToolCallScheduledEvent {
	// Compute a best-effort call hint once at emit time so all subscribers can
	// reuse it. The payload is the canonical JSON arguments; templates that
	// depend on typed structs will be rerun by higher-level decorators (e.g.,
	// the runtime hinting sink) when needed.
	displayHint := rthints.FormatCallHint(toolName, payload)

	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ToolCallScheduledEvent{
		baseEvent:             be,
		ToolCallID:            toolCallID,
		ToolName:              toolName,
		Payload:               payload,
		Queue:                 queue,
		ParentToolCallID:      parentToolCallID,
		ExpectedChildrenTotal: expectedChildren,
		DisplayHint:           displayHint,
	}
}

// NewToolResultReceivedEvent constructs a ToolResultReceivedEvent. Result and
// err capture the tool outcome; duration is the wall-clock execution time;
// telemetry carries structured observability metadata (nil if not collected).
func NewToolResultReceivedEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID, parentToolCallID string, result any, resultPreview string, bounds *agent.Bounds, artifacts []*planner.Artifact, duration time.Duration, telemetry *telemetry.ToolTelemetry, err *toolerrors.ToolError) *ToolResultReceivedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ToolResultReceivedEvent{
		baseEvent:        be,
		ToolCallID:       toolCallID,
		ParentToolCallID: parentToolCallID,
		ToolName:         toolName,
		Result:           result,
		ResultPreview:    resultPreview,
		Bounds:           bounds,
		Artifacts:        artifacts,
		Duration:         duration,
		Telemetry:        telemetry,
		Error:            err,
	}
}

// NewToolCallUpdatedEvent constructs a ToolCallUpdatedEvent to signal that a
// parent tool's child count has increased due to dynamic discovery.
func NewToolCallUpdatedEvent(runID string, agentID agent.Ident, sessionID string, toolCallID string, expectedChildrenTotal int) *ToolCallUpdatedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ToolCallUpdatedEvent{
		baseEvent:             be,
		ToolCallID:            toolCallID,
		ExpectedChildrenTotal: expectedChildrenTotal,
	}
}

// NewUsageEvent constructs a UsageEvent with the provided details.
func NewUsageEvent(runID string, agentID agent.Ident, sessionID string, input, output, total, cacheRead, cacheWrite int) *UsageEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &UsageEvent{
		baseEvent:        be,
		InputTokens:      input,
		OutputTokens:     output,
		TotalTokens:      total,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}
}

// NewHardProtectionEvent constructs a HardProtectionEvent.
func NewHardProtectionEvent(runID string, agentID agent.Ident, sessionID string, reason string, executedAgentTools, childrenTotal int, toolNames []tools.Ident) *HardProtectionEvent {
	names := make([]tools.Ident, len(toolNames))
	copy(names, toolNames)
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &HardProtectionEvent{
		baseEvent:          be,
		Reason:             reason,
		ExecutedAgentTools: executedAgentTools,
		ChildrenTotal:      childrenTotal,
		ToolNames:          names,
	}
}

// NewPlannerNoteEvent constructs a PlannerNoteEvent with the given note text
// and optional labels for categorization.
func NewPlannerNoteEvent(runID string, agentID agent.Ident, sessionID string, note string, labels map[string]string) *PlannerNoteEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &PlannerNoteEvent{
		baseEvent: be,
		Note:      note,
		Labels:    labels,
	}
}

// NewThinkingBlockEvent constructs a ThinkingBlockEvent with structured reasoning fields.
func NewThinkingBlockEvent(runID string, agentID agent.Ident, sessionID string, text, signature string, redacted []byte, contentIndex int, final bool) *ThinkingBlockEvent {
	var rb []byte
	if len(redacted) > 0 {
		rb = append([]byte(nil), redacted...)
	}
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ThinkingBlockEvent{
		baseEvent:    be,
		Text:         text,
		Signature:    signature,
		Redacted:     rb,
		ContentIndex: contentIndex,
		Final:        final,
	}
}

// NewAssistantMessageEvent constructs an AssistantMessageEvent. Structured
// may be nil if only a text message is provided.
func NewAssistantMessageEvent(runID string, agentID agent.Ident, sessionID string, message string, structured any) *AssistantMessageEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &AssistantMessageEvent{
		baseEvent:  be,
		Message:    message,
		Structured: structured,
	}
}

// NewRetryHintIssuedEvent constructs a RetryHintIssuedEvent indicating a
// suggested retry policy adjustment.
func NewRetryHintIssuedEvent(runID string, agentID agent.Ident, sessionID string, reason string, toolName tools.Ident, message string) *RetryHintIssuedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &RetryHintIssuedEvent{
		baseEvent: be,
		Reason:    reason,
		ToolName:  toolName,
		Message:   message,
	}
}

// NewMemoryAppendedEvent constructs a MemoryAppendedEvent indicating successful
// persistence of memory entries.
func NewMemoryAppendedEvent(runID string, agentID agent.Ident, sessionID string, eventCount int) *MemoryAppendedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &MemoryAppendedEvent{
		baseEvent:  be,
		EventCount: eventCount,
	}
}

func classifyNonProviderFailure(err error) (kind, publicError string) {
	var te *temporal.TimeoutError
	if errors.As(err, &te) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorKindTimeout, PublicErrorTimeout
	}
	return ErrorKindInternal, PublicErrorInternal
}

func providerPublicError(pe *model.ProviderError) string {
	switch pe.Kind() {
	case model.ProviderErrorKindRateLimited:
		return PublicErrorProviderRateLimited
	case model.ProviderErrorKindUnavailable:
		return PublicErrorProviderUnavailable
	case model.ProviderErrorKindInvalidRequest:
		return PublicErrorProviderInvalidRequest
	case model.ProviderErrorKindAuth:
		return PublicErrorProviderAuth
	case model.ProviderErrorKindUnknown:
		return PublicErrorProviderUnknown
	default:
		return PublicErrorProviderDefault
	}
}

// RunID returns the workflow run identifier.
func (e baseEvent) RunID() string { return e.runID }

// SessionID returns the logical session identifier associated with the run.
func (e baseEvent) SessionID() string { return e.sessionID }

// AgentID returns the agent identifier.
func (e baseEvent) AgentID() string { return string(e.agentID) }

// Timestamp returns the Unix timestamp in milliseconds when the event occurred.
func (e baseEvent) Timestamp() int64 { return e.timestamp }

// TurnID returns the conversational turn identifier (empty if not set).
func (e baseEvent) TurnID() string { return e.turnID }

// SetTurnID updates the turn identifier. This is called by the runtime to stamp
// events with turn information after construction.
func (e *baseEvent) SetTurnID(turnID string) {
	e.turnID = turnID
}

// SetSessionID updates the session identifier associated with the event. This is
// called by the runtime when constructing events so downstream subscribers can
// rely on SessionID as a stable join key across processes.
func (e *baseEvent) SetSessionID(id string) {
	e.sessionID = id
}

// newBaseEvent constructs a baseEvent with the current timestamp.
func newBaseEvent(runID string, agentID agent.Ident) baseEvent {
	return baseEvent{
		runID:     runID,
		agentID:   agentID,
		timestamp: time.Now().UnixMilli(),
	}
}

// Type method implementations

func (e *RunStartedEvent) Type() EventType         { return RunStarted }
func (e *RunCompletedEvent) Type() EventType       { return RunCompleted }
func (e *RunPausedEvent) Type() EventType          { return RunPaused }
func (e *RunResumedEvent) Type() EventType         { return RunResumed }
func (e *ToolCallScheduledEvent) Type() EventType  { return ToolCallScheduled }
func (e *ToolResultReceivedEvent) Type() EventType { return ToolResultReceived }
func (e *ToolCallUpdatedEvent) Type() EventType    { return ToolCallUpdated }
func (e *PlannerNoteEvent) Type() EventType        { return PlannerNote }
func (e *AssistantMessageEvent) Type() EventType   { return AssistantMessage }
func (e *ThinkingBlockEvent) Type() EventType      { return ThinkingBlock }
func (e *RetryHintIssuedEvent) Type() EventType    { return RetryHintIssued }
func (e *MemoryAppendedEvent) Type() EventType     { return MemoryAppended }
func (e *PolicyDecisionEvent) Type() EventType     { return PolicyDecision }
func (e *UsageEvent) Type() EventType              { return Usage }
func (e *HardProtectionEvent) Type() EventType     { return HardProtectionTriggered }
func (e *RunPhaseChangedEvent) Type() EventType    { return RunPhaseChanged }
func (e *AgentRunStartedEvent) Type() EventType    { return AgentRunStarted }
