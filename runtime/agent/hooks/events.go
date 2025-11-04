package hooks

import (
	"time"

	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/toolerrors"
	"goa.design/goa-ai/runtime/agent/tools"
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
		// SeqInTurn returns the monotonic sequence number of this event within its turn,
		// starting at 0. When TurnID is empty, SeqInTurn is 0. This ordering allows
		// deterministic replay and helps UIs render events in the correct order even when
		// delivery is out of sequence.
		SeqInTurn() int
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
		// Error contains any terminal error that halted the run. Nil on success.
		Error error
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

	// ToolCallScheduledEvent fires when the runtime schedules a tool activity
	// for execution.
	ToolCallScheduledEvent struct {
		baseEvent
		// ToolCallID uniquely identifies the scheduled tool invocation so progress
		// updates can correlate with the original request.
		ToolCallID string
		// ToolName is the fully qualified tool identifier (e.g., "service.toolset.tool").
		ToolName tools.Ident
		// Payload contains the structured tool arguments (JSON-serializable) for the scheduled tool.
		// Not pre-encoded; sinks/transports encode it for wire format.
		Payload any
		// Queue is the activity queue name where the tool execution is scheduled.
		Queue string
		// ParentToolCallID optionally identifies the tool call that requested this tool.
		// Empty for top-level planner-requested tools. Used to track parent-child chains.
		ParentToolCallID string
		// ExpectedChildrenTotal indicates how many child tools are expected from this batch.
		// A value of 0 means no children expected or count not tracked by the planner.
		ExpectedChildrenTotal int
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
		// ToolName is the fully qualified tool identifier that was executed.
		ToolName tools.Ident
		// Result contains the tool's output payload. Nil if Error is set.
		Result any
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
		// AllowedTools lists the fully qualified tool identifiers that the policy engine
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
	// the RunID, AgentID, Timestamp, TurnID, and SeqInTurn methods.
	baseEvent struct {
		runID     string
		agentID   string
		timestamp int64
		// turnID identifies the conversational turn this event belongs to (optional).
		// When set, groups events for UI rendering and conversation tracking.
		turnID string
		// seqInTurn is the monotonic sequence number within the turn, starting at 0.
		// Used to order events deterministically for display and debugging.
		seqInTurn int
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
		Payload    any
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
	}
)

// NewRunStartedEvent constructs a RunStartedEvent with the current
// timestamp. RunContext and Input capture the initial run state.
func NewRunStartedEvent(runID, agentID string, runContext run.Context, input any) *RunStartedEvent {
	return &RunStartedEvent{
		baseEvent:  newBaseEvent(runID, agentID),
		RunContext: runContext,
		Input:      input,
	}
}

// NewRunCompletedEvent constructs a RunCompletedEvent. Status should
// be "success", "failed", or "canceled"; err may be nil on success.
func NewRunCompletedEvent(runID, agentID, status string, err error) *RunCompletedEvent {
	return &RunCompletedEvent{
		baseEvent: newBaseEvent(runID, agentID),
		Status:    status,
		Error:     err,
	}
}

// NewRunPausedEvent constructs a RunPausedEvent with provided metadata.
func NewRunPausedEvent(
	runID, agentID, reason, requestedBy string,
	labels map[string]string,
	metadata map[string]any,
) *RunPausedEvent {
	return &RunPausedEvent{
		baseEvent:   newBaseEvent(runID, agentID),
		Reason:      reason,
		RequestedBy: requestedBy,
		Labels:      labels,
		Metadata:    metadata,
	}
}

// NewRunResumedEvent constructs a RunResumedEvent with provided metadata.
func NewRunResumedEvent(
	runID, agentID, notes, requestedBy string,
	labels map[string]string,
	messageCount int,
) *RunResumedEvent {
	return &RunResumedEvent{
		baseEvent:    newBaseEvent(runID, agentID),
		Notes:        notes,
		RequestedBy:  requestedBy,
		Labels:       labels,
		MessageCount: messageCount,
	}
}

// NewAwaitClarificationEvent constructs an AwaitClarificationEvent with the provided details.
func NewAwaitClarificationEvent(
	runID, agentID, id, question string,
	missing []string,
	restrict tools.Ident,
	example map[string]any,
) *AwaitClarificationEvent {
	var ex map[string]any
	if len(example) > 0 {
		ex = make(map[string]any, len(example))
		for k, v := range example {
			ex[k] = v
		}
	}
	return &AwaitClarificationEvent{
		baseEvent:      newBaseEvent(runID, agentID),
		ID:             id,
		Question:       question,
		MissingFields:  append([]string(nil), missing...),
		RestrictToTool: restrict,
		ExampleInput:   ex,
	}
}

// NewAwaitExternalToolsEvent constructs an AwaitExternalToolsEvent.
func NewAwaitExternalToolsEvent(
	runID, agentID, id string,
	items []AwaitToolItem,
) *AwaitExternalToolsEvent {
	// ensure copy
	copied := make([]AwaitToolItem, len(items))
	copy(copied, items)
	return &AwaitExternalToolsEvent{
		baseEvent: newBaseEvent(runID, agentID),
		ID:        id,
		Items:     copied,
	}
}

// NewPolicyDecisionEvent constructs a PolicyDecisionEvent with the provided metadata.
func NewPolicyDecisionEvent(
	runID, agentID string,
	allowed []tools.Ident,
	caps policy.CapsState,
	labels map[string]string,
	metadata map[string]any,
) *PolicyDecisionEvent {
	return &PolicyDecisionEvent{
		baseEvent:    newBaseEvent(runID, agentID),
		AllowedTools: allowed,
		Caps:         caps,
		Labels:       labels,
		Metadata:     metadata,
	}
}

// Type implements Event for AwaitClarificationEvent.
func (e *AwaitClarificationEvent) Type() EventType { return AwaitClarification }

// Type implements Event for AwaitExternalToolsEvent.
func (e *AwaitExternalToolsEvent) Type() EventType { return AwaitExternalTools }

// NewToolCallScheduledEvent constructs a ToolCallScheduledEvent. Payload is the
// structured tool arguments (JSON-serializable); queue is the activity queue name.
// ParentToolCallID and expectedChildren are optional (empty/0 for top-level calls).
func NewToolCallScheduledEvent(
	runID, agentID string,
	toolName tools.Ident, toolCallID string,
	payload any,
	queue string,
	parentToolCallID string,
	expectedChildren int,
) *ToolCallScheduledEvent {
	return &ToolCallScheduledEvent{
		baseEvent:             newBaseEvent(runID, agentID),
		ToolCallID:            toolCallID,
		ToolName:              toolName,
		Payload:               payload,
		Queue:                 queue,
		ParentToolCallID:      parentToolCallID,
		ExpectedChildrenTotal: expectedChildren,
	}
}

// NewToolResultReceivedEvent constructs a ToolResultReceivedEvent. Result and
// err capture the tool outcome; duration is the wall-clock execution time;
// telemetry carries structured observability metadata (nil if not collected).
func NewToolResultReceivedEvent(
	runID, agentID string,
	toolName tools.Ident,
	toolCallID, parentToolCallID string,
	result any,
	duration time.Duration,
	telemetry *telemetry.ToolTelemetry,
	err *toolerrors.ToolError,
) *ToolResultReceivedEvent {
	return &ToolResultReceivedEvent{
		baseEvent:        newBaseEvent(runID, agentID),
		ToolCallID:       toolCallID,
		ParentToolCallID: parentToolCallID,
		ToolName:         toolName,
		Result:           result,
		Duration:         duration,
		Telemetry:        telemetry,
		Error:            err,
	}
}

// NewToolCallUpdatedEvent constructs a ToolCallUpdatedEvent to signal that a
// parent tool's child count has increased due to dynamic discovery.
func NewToolCallUpdatedEvent(runID, agentID string, toolCallID string, expectedChildrenTotal int) *ToolCallUpdatedEvent {
	return &ToolCallUpdatedEvent{
		baseEvent:             newBaseEvent(runID, agentID),
		ToolCallID:            toolCallID,
		ExpectedChildrenTotal: expectedChildrenTotal,
	}
}

// NewUsageEvent constructs a UsageEvent with the provided details.
func NewUsageEvent(runID, agentID string, input, output, total int) *UsageEvent {
	return &UsageEvent{
		baseEvent:    newBaseEvent(runID, agentID),
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  total,
	}
}

// NewPlannerNoteEvent constructs a PlannerNoteEvent with the given note text
// and optional labels for categorization.
func NewPlannerNoteEvent(runID, agentID, note string, labels map[string]string) *PlannerNoteEvent {
	return &PlannerNoteEvent{
		baseEvent: newBaseEvent(runID, agentID),
		Note:      note,
		Labels:    labels,
	}
}

// NewAssistantMessageEvent constructs an AssistantMessageEvent. Structured
// may be nil if only a text message is provided.
func NewAssistantMessageEvent(runID, agentID, message string, structured any) *AssistantMessageEvent {
	return &AssistantMessageEvent{
		baseEvent:  newBaseEvent(runID, agentID),
		Message:    message,
		Structured: structured,
	}
}

// NewRetryHintIssuedEvent constructs a RetryHintIssuedEvent indicating a
// suggested retry policy adjustment.
func NewRetryHintIssuedEvent(runID, agentID, reason string, toolName tools.Ident, message string) *RetryHintIssuedEvent {
	return &RetryHintIssuedEvent{
		baseEvent: newBaseEvent(runID, agentID),
		Reason:    reason,
		ToolName:  toolName,
		Message:   message,
	}
}

// NewMemoryAppendedEvent constructs a MemoryAppendedEvent indicating successful
// persistence of memory entries.
func NewMemoryAppendedEvent(runID, agentID string, eventCount int) *MemoryAppendedEvent {
	return &MemoryAppendedEvent{
		baseEvent:  newBaseEvent(runID, agentID),
		EventCount: eventCount,
	}
}

// RunID returns the workflow run identifier.
func (e baseEvent) RunID() string { return e.runID }

// AgentID returns the agent identifier.
func (e baseEvent) AgentID() string { return e.agentID }

// Timestamp returns the Unix timestamp in milliseconds when the event occurred.
func (e baseEvent) Timestamp() int64 { return e.timestamp }

// TurnID returns the conversational turn identifier (empty if not set).
func (e baseEvent) TurnID() string { return e.turnID }

// SeqInTurn returns the monotonic sequence number within the turn.
func (e baseEvent) SeqInTurn() int { return e.seqInTurn }

// SetTurn updates the turn tracking fields. This is called by the runtime to stamp
// events with turn sequencing information after construction.
func (e *baseEvent) SetTurn(turnID string, seqInTurn int) {
	e.turnID = turnID
	e.seqInTurn = seqInTurn
}

// newBaseEvent constructs a baseEvent with the current timestamp.
func newBaseEvent(runID, agentID string) baseEvent {
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
func (e *RetryHintIssuedEvent) Type() EventType    { return RetryHintIssued }
func (e *MemoryAppendedEvent) Type() EventType     { return MemoryAppended }
func (e *PolicyDecisionEvent) Type() EventType     { return PolicyDecision }
func (e *UsageEvent) Type() EventType              { return Usage }
