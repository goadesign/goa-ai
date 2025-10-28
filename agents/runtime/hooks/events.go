package hooks

import (
	"time"

	"goa.design/goa-ai/agents/runtime/policy"
	"goa.design/goa-ai/agents/runtime/run"
	"goa.design/goa-ai/agents/runtime/telemetry"
	"goa.design/goa-ai/agents/runtime/toolerrors"
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
		Type() EventType
		RunID() string
		AgentID() string
		Timestamp() int64
		TurnID() string
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
		Reason      string
		RequestedBy string
		Labels      map[string]string
		Metadata    map[string]any
	}

	// RunResumedEvent fires when a paused run resumes.
	RunResumedEvent struct {
		baseEvent
		Notes        string
		RequestedBy  string
		Labels       map[string]string
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
		ToolName string
		// Payload contains the marshaled tool arguments passed to the activity.
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
		// ToolName is the fully qualified tool identifier that was executed.
		ToolName string
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
		ToolName string
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
		AllowedTools []string
		Caps         policy.CapsState
		Labels       map[string]string
		Metadata     map[string]any
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

// NewPolicyDecisionEvent constructs a PolicyDecisionEvent with the provided metadata.
func NewPolicyDecisionEvent(
	runID, agentID string,
	allowed []string,
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

// NewToolCallScheduledEvent constructs a ToolCallScheduledEvent. Payload is
// the marshaled tool arguments; queue is the activity queue name. ParentToolCallID
// and expectedChildren are optional (empty/0 for top-level calls).
func NewToolCallScheduledEvent(
	runID, agentID, toolName, toolCallID string,
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
	runID, agentID, toolName string,
	result any,
	duration time.Duration,
	telemetry *telemetry.ToolTelemetry,
	err *toolerrors.ToolError,
) *ToolResultReceivedEvent {
	return &ToolResultReceivedEvent{
		baseEvent: newBaseEvent(runID, agentID),
		ToolName:  toolName,
		Result:    result,
		Duration:  duration,
		Telemetry: telemetry,
		Error:     err,
	}
}

// NewToolCallUpdatedEvent constructs a ToolCallUpdatedEvent to signal that a
// parent tool's child count has increased due to dynamic discovery.
func NewToolCallUpdatedEvent(
	runID, agentID, toolCallID string,
	expectedChildrenTotal int,
) *ToolCallUpdatedEvent {
	return &ToolCallUpdatedEvent{
		baseEvent:             newBaseEvent(runID, agentID),
		ToolCallID:            toolCallID,
		ExpectedChildrenTotal: expectedChildrenTotal,
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
func NewRetryHintIssuedEvent(runID, agentID, reason, toolName, message string) *RetryHintIssuedEvent {
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
