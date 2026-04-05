package hooks

import (
	"context"
	"errors"
	"fmt"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/toolerrors"
	"goa.design/goa-ai/runtime/agent/tools"
	"time"

	"go.temporal.io/sdk/temporal"
)

const providerErrorApplicationType = "goa_ai.provider_error"

type (
	providerErrorEnvelope struct {
		Provider   string
		Operation  string
		HTTPStatus int
		Kind       string
		Code       string
		Message    string
		RequestID  string
		Retryable  bool
	}

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
		// EventKey returns the stable logical identity for this event within the run.
		// Durable stores use it to make canonical event publishing exact-once even when
		// the hook activity is retried.
		EventKey() string
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
		// Failure carries the terminal failure payload when Status is "failed".
		// It is nil on successful and canceled runs.
		Failure *run.Failure
		// Cancellation carries the terminal cancellation payload when Status is
		// "canceled". It is nil on successful and failed runs.
		Cancellation *run.Cancellation
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

	// ChildRunLinkedEvent links a parent run/tool call to a spawned child agent run.
	// It is emitted on the parent run and allows consumers to correlate child-run
	// events without flattening them into the parent.
	ChildRunLinkedEvent struct {
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

	// PromptRenderedEvent fires when the runtime resolves and renders a prompt.
	PromptRenderedEvent struct {
		baseEvent
		// PromptID identifies the rendered prompt specification.
		PromptID prompt.Ident
		// Version is the resolved prompt version used for rendering.
		Version string
		// Scope is the resolved override scope used during prompt resolution.
		Scope prompt.Scope
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
		Payload rawjson.Message
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
		// ResultJSON contains the canonical JSON encoding of the tool result as produced
		// by the tool's generated result codec.
		//
		// This is the canonical durable representation consumed by stream sinks,
		// persistence layers, and planner resume hydration.
		ResultJSON rawjson.Message
		// ResultBytes is the size, in bytes, of the canonical JSON result payload
		// before any workflow-boundary omission is applied.
		ResultBytes int
		// ResultOmitted indicates that the canonical result payload was omitted
		// from the workflow-safe envelope that produced this event.
		ResultOmitted bool
		// ResultOmittedReason provides a stable, machine-readable reason for omitting
		// the canonical result payload. Empty when ResultOmitted is false.
		ResultOmittedReason string
		// ServerData carries server-only data emitted by tool providers. This payload
		// must not be serialized into model provider requests and is treated as opaque
		// JSON bytes by the runtime.
		ServerData rawjson.Message
		// ResultPreview is a concise, user-facing summary of the tool result rendered
		// from the registered ResultHintTemplate for this tool. Result templates
		// receive the runtime preview wrapper (`.Args` for typed payload data,
		// `.Result` for semantic data, `.Bounds` for bounded-result metadata). The
		// preview is computed while the result is still strongly typed so
		// downstream subscribers do not need to re-render templates from
		// JSON-decoded maps.
		ResultPreview string
		// Bounds, when non-nil, describes how the tool result has been bounded
		// relative to the full underlying data set. It is supplied by tool
		// implementations and surfaced for observability; the runtime does not
		// modify it.
		Bounds *agent.Bounds
		// Duration is the wall-clock execution time for the tool activity.
		Duration time.Duration
		// Telemetry holds structured observability metadata (tokens, model, retries).
		// Nil if no telemetry was collected.
		Telemetry *telemetry.ToolTelemetry
		// RetryHint carries structured guidance for recovering from tool failures.
		// It is typically populated for validation/repair flows (missing fields,
		// invalid arguments) and surfaced to clients so they can prompt the user
		// and retry deterministically.
		RetryHint *planner.RetryHint
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

	// ToolCallArgsDeltaEvent fires when a provider streams an incremental tool-call
	// argument fragment while constructing the final tool input JSON.
	//
	// Contract:
	//   - This event is best-effort and may be ignored or dropped entirely.
	//   - Delta is not guaranteed to be valid JSON on its own.
	//   - The canonical tool payload is still emitted via ToolCallScheduledEvent
	//     and ToolResultReceivedEvent (and, at the model boundary, the finalized
	//     tool call chunk).
	ToolCallArgsDeltaEvent struct {
		baseEvent
		// ToolCallID is the provider-issued identifier for the tool call.
		ToolCallID string
		// ToolName is the canonical tool identifier when known.
		ToolName tools.Ident
		// Delta is a raw JSON fragment emitted while streaming tool input JSON.
		Delta string
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
		eventKey  string
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
		Payload rawjson.Message
	}

	// AwaitQuestionsEvent indicates the planner requested structured multiple-choice
	// answers to be provided out-of-band (typically by a UI) before the run can resume.
	AwaitQuestionsEvent struct {
		baseEvent
		// ID correlates this await with a subsequent ProvideToolResults.
		ID string
		// ToolName identifies the tool awaiting user answers.
		ToolName tools.Ident
		// ToolCallID correlates the provided result with this requested call.
		ToolCallID string
		// Payload is the canonical JSON arguments for the awaited tool call.
		Payload rawjson.Message
		// Title is an optional display title for the questions UI.
		Title *string
		// Questions are the structured questions to present to the user.
		Questions []AwaitQuestion
	}

	// AwaitQuestion describes a single multiple-choice question.
	AwaitQuestion struct {
		ID            string
		Prompt        string
		Options       []AwaitQuestionOption
		AllowMultiple bool
	}

	// AwaitQuestionOption describes a selectable answer option.
	AwaitQuestionOption struct {
		ID    string
		Label string
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
		Payload    rawjson.Message
	}

	// UsageEvent reports token usage for a model invocation within a run.
	// Emitted when the model stream reports usage deltas or a final summary.
	UsageEvent struct {
		baseEvent
		// TokenUsage contains the attributed token counts reported by the model
		// adapter. Model and ModelClass identify the specific model that produced
		// this delta.
		model.TokenUsage
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

// NewRunCompletedEvent constructs a RunCompletedEvent.
//
// Contract:
//   - Status must be "success", "failed", or "canceled".
//   - Failed runs must provide a non-nil err so Failure can be populated.
//   - Canceled runs should provide cancellation provenance; when none is supplied,
//     the runtime records the cancel as engine-originated.
func NewRunCompletedEvent(
	runID string,
	agentID agent.Ident,
	sessionID, status string,
	phase run.Phase,
	err error,
	cancellation *run.Cancellation,
) *RunCompletedEvent {
	var failure *run.Failure
	switch status {
	case "failed":
		if err != nil {
			failure = newRunFailure(err)
		}
	case "canceled":
		cancellation = newRunCancellation(cancellation)
	}
	evt, buildErr := newRunCompletedEventFromPayload(runID, agentID, sessionID, status, phase, failure, cancellation)
	if buildErr != nil {
		panic("hooks: " + buildErr.Error())
	}
	return evt
}

// newRunCompletedEventFromPayload validates the canonical terminal outcome
// payload before materializing a RunCompletedEvent from decoded hook input.
func newRunCompletedEventFromPayload(
	runID string,
	agentID agent.Ident,
	sessionID, status string,
	phase run.Phase,
	failure *run.Failure,
	cancellation *run.Cancellation,
) (*RunCompletedEvent, error) {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	out := &RunCompletedEvent{
		baseEvent: be,
		Status:    status,
		Phase:     phase,
	}
	switch status {
	case "success":
		if failure != nil || cancellation != nil {
			return nil, errors.New("successful run completion must not carry terminal outcome payload")
		}
		return out, nil
	case "failed":
		if cancellation != nil {
			return nil, errors.New("failed run completion must not carry cancellation payload")
		}
		if failure == nil {
			return nil, errors.New("failed run completion requires failure payload")
		}
		if failure.Message == "" {
			return nil, errors.New("failed run completion requires failure message")
		}
		if failure.Kind == "" {
			return nil, errors.New("failed run completion requires failure kind")
		}
		out.Failure = failure
		return out, nil
	case "canceled":
		if failure != nil {
			return nil, errors.New("canceled run completion must not carry failure payload")
		}
		if cancellation == nil {
			return nil, errors.New("canceled run completion requires cancellation payload")
		}
		if cancellation.Reason == "" {
			return nil, errors.New("canceled run completion requires cancellation reason")
		}
		out.Cancellation = cancellation
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected run completion status %q", status)
	}
}

// newRunFailure classifies the terminal error into the canonical failure payload.
func newRunFailure(err error) *run.Failure {
	if pe, ok := providerErrorFromError(err); ok {
		return &run.Failure{
			Message:      providerPublicError(pe),
			DebugMessage: err.Error(),
			Provider:     pe.Provider(),
			Operation:    pe.Operation(),
			Kind:         string(pe.Kind()),
			Code:         pe.Code(),
			HTTPStatus:   pe.HTTPStatus(),
			Retryable:    pe.Retryable(),
		}
	}
	kind, message := classifyNonProviderFailure(err)
	return &run.Failure{
		Message:      message,
		DebugMessage: err.Error(),
		Kind:         kind,
		Retryable:    true, // Non-provider failures are always retryable.
	}
}

// newRunCancellation returns the canonical cancellation payload for a terminal
// canceled run.
func newRunCancellation(cancellation *run.Cancellation) *run.Cancellation {
	if cancellation == nil {
		return &run.Cancellation{
			Reason: run.CancellationReasonEngineCanceled,
		}
	}
	if cancellation.Reason == "" {
		panic("hooks: canceled run completion requires a cancellation reason")
	}
	return &run.Cancellation{
		Reason: cancellation.Reason,
	}
}

// WrapRunCompletionError encodes provider failures into a Temporal application
// error envelope so Wait()/Get()-based terminal paths can recover structured
// provider metadata after the workflow engine serializes the error.
func WrapRunCompletionError(err error) error {
	if _, alreadyWrapped := providerErrorFromTemporalEnvelope(err); alreadyWrapped {
		return err
	}
	pe, ok := model.AsProviderError(err)
	if !ok {
		return err
	}
	envelope := providerErrorEnvelope{
		Provider:   pe.Provider(),
		Operation:  pe.Operation(),
		HTTPStatus: pe.HTTPStatus(),
		Kind:       string(pe.Kind()),
		Code:       pe.Code(),
		Message:    pe.Message(),
		RequestID:  pe.RequestID(),
		Retryable:  pe.Retryable(),
	}
	if pe.Retryable() {
		return temporal.NewApplicationErrorWithCause(pe.Error(), providerErrorApplicationType, err, envelope)
	}
	return temporal.NewNonRetryableApplicationError(pe.Error(), providerErrorApplicationType, err, envelope)
}

// NewChildRunLinkedEvent constructs a ChildRunLinkedEvent for the given parent
// run, tool call, and child run identifiers.
func NewChildRunLinkedEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID, childRunID string, childAgentID agent.Ident) *ChildRunLinkedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ChildRunLinkedEvent{
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

// NewPromptRenderedEvent constructs a PromptRenderedEvent for one rendered prompt.
func NewPromptRenderedEvent(runID string, agentID agent.Ident, sessionID string, promptID prompt.Ident, version string, scope prompt.Scope) *PromptRenderedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &PromptRenderedEvent{
		baseEvent: be,
		PromptID:  promptID,
		Version:   version,
		Scope:     scope,
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
func NewAwaitConfirmationEvent(runID string, agentID agent.Ident, sessionID, id, title, prompt string, toolName tools.Ident, toolCallID string, payload rawjson.Message) *AwaitConfirmationEvent {
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

// NewAwaitQuestionsEvent constructs an AwaitQuestionsEvent for a structured questions prompt.
func NewAwaitQuestionsEvent(runID string, agentID agent.Ident, sessionID, id string, toolName tools.Ident, toolCallID string, payload rawjson.Message, title *string, questions []AwaitQuestion) *AwaitQuestionsEvent {
	qcopy := make([]AwaitQuestion, 0, len(questions))
	for _, q := range questions {
		opts := make([]AwaitQuestionOption, len(q.Options))
		copy(opts, q.Options)
		qcopy = append(qcopy, AwaitQuestion{
			ID:            q.ID,
			Prompt:        q.Prompt,
			AllowMultiple: q.AllowMultiple,
			Options:       opts,
		})
	}
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &AwaitQuestionsEvent{
		baseEvent:  be,
		ID:         id,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Payload:    payload,
		Title:      title,
		Questions:  qcopy,
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

// Type implements Event for AwaitQuestionsEvent.
func (e *AwaitQuestionsEvent) Type() EventType { return AwaitQuestions }

// Type implements Event for AwaitExternalToolsEvent.
func (e *AwaitExternalToolsEvent) Type() EventType { return AwaitExternalTools }

// NewToolCallScheduledEvent constructs a ToolCallScheduledEvent. Payload is the
// canonical JSON arguments for the scheduled tool; queue is the activity queue name.
// ParentToolCallID and expectedChildren are optional (empty/0 for top-level calls).
func NewToolCallScheduledEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID string, payload rawjson.Message, queue string, parentToolCallID string, expectedChildren int) *ToolCallScheduledEvent {
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
		// DisplayHint is computed by the runtime at publish time using typed payloads
		// and registered templates. This keeps the contract strict: hints are never
		// rendered against raw JSON bytes.
		DisplayHint: "",
	}
}

// NewToolResultReceivedEvent constructs a ToolResultReceivedEvent. The canonical
// result JSON and server-side sidecars are stored exactly once here; duration is
// the wall-clock execution time; telemetry carries structured observability
// metadata (nil if not collected).
func NewToolResultReceivedEvent(runID string, agentID agent.Ident, sessionID string, toolName tools.Ident, toolCallID, parentToolCallID string, resultJSON rawjson.Message, resultBytes int, resultOmitted bool, resultOmittedReason string, serverData rawjson.Message, resultPreview string, bounds *agent.Bounds, duration time.Duration, telemetry *telemetry.ToolTelemetry, retryHint *planner.RetryHint, err *toolerrors.ToolError) *ToolResultReceivedEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ToolResultReceivedEvent{
		baseEvent:           be,
		ToolCallID:          toolCallID,
		ParentToolCallID:    parentToolCallID,
		ToolName:            toolName,
		ResultJSON:          resultJSON,
		ResultBytes:         resultBytes,
		ResultOmitted:       resultOmitted,
		ResultOmittedReason: resultOmittedReason,
		ServerData:          serverData,
		ResultPreview:       resultPreview,
		Bounds:              bounds,
		Duration:            duration,
		Telemetry:           telemetry,
		RetryHint:           retryHint,
		Error:               err,
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

// NewToolCallArgsDeltaEvent constructs a ToolCallArgsDeltaEvent.
func NewToolCallArgsDeltaEvent(runID string, agentID agent.Ident, sessionID string, toolCallID string, toolName tools.Ident, delta string) *ToolCallArgsDeltaEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &ToolCallArgsDeltaEvent{
		baseEvent:  be,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Delta:      delta,
	}
}

// NewUsageEvent constructs a UsageEvent from an attributed usage snapshot.
func NewUsageEvent(runID string, agentID agent.Ident, sessionID string, usage model.TokenUsage) *UsageEvent {
	be := newBaseEvent(runID, agentID)
	be.sessionID = sessionID
	return &UsageEvent{
		baseEvent:  be,
		TokenUsage: usage,
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

func providerErrorFromError(err error) (*model.ProviderError, bool) {
	pe, ok := model.AsProviderError(err)
	if ok {
		return pe, true
	}
	return providerErrorFromTemporalEnvelope(err)
}

func providerErrorFromTemporalEnvelope(err error) (*model.ProviderError, bool) {
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		return nil, false
	}
	if appErr.Type() != providerErrorApplicationType {
		return nil, false
	}
	var envelope providerErrorEnvelope
	decoded := appErr.Details(&envelope) == nil
	if !decoded {
		return nil, false
	}
	if envelope.Provider == "" || envelope.Kind == "" {
		return nil, false
	}
	return model.NewProviderError(
		envelope.Provider,
		envelope.Operation,
		envelope.HTTPStatus,
		model.ProviderErrorKind(envelope.Kind),
		envelope.Code,
		envelope.Message,
		envelope.RequestID,
		envelope.Retryable,
		appErr,
	), true
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

// EventKey returns the stable logical identity for this event.
func (e baseEvent) EventKey() string { return e.eventKey }

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

// SetTimestampMS restores the original event timestamp when reconstructing an
// event from a durable runtime record envelope.
func (e *baseEvent) SetTimestampMS(timestampMS int64) {
	e.timestamp = timestampMS
}

// SetEventKey restores the original event key when reconstructing an event from
// a durable runtime record envelope.
func (e *baseEvent) SetEventKey(eventKey string) {
	e.eventKey = eventKey
}

// newBaseEvent constructs a baseEvent without durable dispatch metadata. The
// runtime stamps event keys and timestamps when it emits the enclosing record.
func newBaseEvent(runID string, agentID agent.Ident) baseEvent {
	return baseEvent{
		runID:   runID,
		agentID: agentID,
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
func (e *ToolCallArgsDeltaEvent) Type() EventType  { return ToolCallArgsDelta }
func (e *PlannerNoteEvent) Type() EventType        { return PlannerNote }
func (e *AssistantMessageEvent) Type() EventType   { return AssistantMessage }
func (e *ThinkingBlockEvent) Type() EventType      { return ThinkingBlock }
func (e *RetryHintIssuedEvent) Type() EventType    { return RetryHintIssued }
func (e *MemoryAppendedEvent) Type() EventType     { return MemoryAppended }
func (e *PolicyDecisionEvent) Type() EventType     { return PolicyDecision }
func (e *UsageEvent) Type() EventType              { return Usage }
func (e *HardProtectionEvent) Type() EventType     { return HardProtectionTriggered }
func (e *RunPhaseChangedEvent) Type() EventType    { return RunPhaseChanged }
func (e *ChildRunLinkedEvent) Type() EventType     { return ChildRunLinked }
func (e *PromptRenderedEvent) Type() EventType     { return PromptRendered }
