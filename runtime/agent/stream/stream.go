// Package stream provides abstractions for delivering real-time agent execution
// updates to clients. Stream events differ from hook events: stream events are
// client-facing updates (tool progress, assistant replies) while hook events
// provide comprehensive internal observability across the entire runtime lifecycle.
//
// The StreamSubscriber in the hooks package bridges selected hook events into
// stream events, filtering out internal-only events (policy decisions, memory
// operations) and transforming runtime state into wire-friendly payloads suitable
// for Server-Sent Events, WebSockets, or message buses like Pulse.
//
// All event types implement the Event interface and can be safely sent concurrently
// through a Sink implementation. Implementations are responsible for marshaling
// events into their wire format (JSON, protobuf, etc.).
package stream

import (
	"context"
	"encoding/json"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/toolerrors"
)

type (
	// Sink delivers streaming updates to clients over a transport (SSE, WebSocket, Pulse).
	// Implementations must be thread-safe: the runtime may call Send concurrently from
	// multiple goroutines when streaming tool results or planner thoughts in parallel.
	//
	// Naming note: Send belongs to the sink (the transmitter), not the subscriber.
	// The hooks.StreamSubscriber RECEIVES events from the internal bus and forwards
	// them by invoking Sink.Send. Transports and tests implement Sink; typical
	// application code does not call Send directly unless it is acting as a transport.
	Sink interface {
		// Send publishes an event to the sink's underlying transport. The implementation
		// is responsible for marshaling the event into the wire format and handling
		// transport-specific delivery semantics (retry, buffering, backpressure).
		//
		// Send should return an error if delivery fails (connection closed, serialization
		// error, transport unavailable). The runtime propagates Send errors to the hook
		// bus, which stops event delivery to remaining subscribers, ensuring streaming
		// failures surface immediately rather than silently dropping events.
		//
		// Thread-safe: safe to call concurrently from multiple goroutines.
		Send(ctx context.Context, event Event) error

		// Close releases resources owned by the sink (connections, buffers, background
		// goroutines). After Close returns, subsequent Send calls must return errors.
		//
		// Close is idempotent: calling it multiple times is safe and has no effect after
		// the first call. Implementations should block until all pending events are
		// flushed or ctx is canceled.
		//
		// The context controls the maximum time allowed for graceful shutdown. If ctx
		// expires before shutdown completes, implementations should abort immediately,
		// potentially dropping unflushed events.
		Close(ctx context.Context) error
	}

	// Event describes a streaming event delivered to clients through a Sink. All concrete
	// event types embed Base to provide standard metadata (type, run ID, payload). Sinks
	// use the Event interface to marshal events generically; consumers can type-assert to
	// concrete types when they need structured field access.
	//
	// Implementations are immutable after construction and safe to send concurrently.
	Event interface {
		// Type returns the event type constant (e.g., EventToolEnd, EventAssistantReply).
		// Subscribers use this to filter events by category or route to type-specific
		// handlers without performing type assertions. For example, a UI might subscribe
		// only to EventAssistantReply and EventToolEnd to display final outputs while
		// ignoring planner thoughts.
		Type() EventType

		// RunID returns the unique workflow run identifier that produced this event. All
		// events within a single run execution share the same run ID, enabling clients to
		// filter or group events by run. This is critical for multi-tenant systems where
		// a single Sink may multiplex events from multiple concurrent runs.
		RunID() string

		// SessionID returns the logical session identifier associated with the run.
		// All events for a given run share the same session ID, providing a stable
		// join key across processes and transports.
		SessionID() string

		// Payload returns the event-specific data in a JSON-serializable form. Sinks use
		// this for generic marshaling when they don't need typed access. For example, the
		// Pulse sink calls Payload() and marshals the result to JSON without knowing the
		// concrete event type.
		//
		// Consumers that need structured access to event fields (e.g., SSE adapters mapping
		// to Goa transport types) should use type assertions on the Event itself to access
		// fields like ToolStart.Data or AssistantReply.Text directly. Use Payload() for
		// generic serialization; use type assertions for type-safe field access.
		Payload() any
	}

	// AssistantReply streams incremental assistant message content as the planner
	// produces the final response. Clients receive these events to display streaming
	// text with a typewriter effect. Multiple AssistantReply events may be sent for
	// a single response as the planner generates content progressively.
	AssistantReply struct {
		Base
		// Data contains the assistant reply payload. Clients should concatenate
		// Data.Text from sequential AssistantReply events to reconstruct the
		// full message.
		Data AssistantReplyPayload
	}

	// PlannerThought streams planner reasoning and intermediate annotations during
	// execution. These events allow clients to display "thinking..." indicators and
	// show the planner's internal reasoning process before tool calls complete.
	PlannerThought struct {
		Base
		// Data contains the planner thought payload (e.g., "Analyzing user query...",
		// "Calling weather API to get forecast..."). This is human-readable text
		// suitable for displaying in a UI thought bubble or debug panel.
		Data PlannerThoughtPayload
	}

	// ToolStart streams when the runtime schedules a tool activity for execution. Clients
	// receive this before the tool executes, allowing UIs to display pending tool calls,
	// show progress indicators, and prepare to receive the corresponding ToolEnd event.
	ToolStart struct {
		Base
		// Data contains the structured metadata for this tool invocation. Clients access
		// this field directly for type-safe field access (e.g., event.Data.ToolCallID).
		Data ToolStartPayload
	}

	// ToolUpdate streams progress updates for a tool call (new expected child count).
	ToolUpdate struct {
		Base
		Data ToolUpdatePayload
	}

	// ToolCallArgsDelta streams an incremental tool-call argument fragment as the
	// provider constructs the final tool input JSON.
	//
	// Contract:
	//   - This is a best-effort UX signal. Consumers may ignore it entirely.
	//   - Delta fragments are not guaranteed to be valid JSON on their own.
	//   - The canonical tool payload is still emitted via ToolStartPayload.Payload
	//     and the final tool call completion events.
	ToolCallArgsDelta struct {
		Base
		Data ToolCallArgsDeltaPayload
	}

	// ToolOutputDelta streams an incremental tool output fragment while the tool
	// is still running.
	//
	// Contract:
	//   - This is a best-effort UX signal. Consumers may ignore it entirely.
	//   - The canonical tool output is still emitted via ToolEnd.
	ToolOutputDelta struct {
		Base
		Data ToolOutputDeltaPayload
	}

	// ToolEnd streams when a tool activity completes with either a result or error. Clients
	// receive this to update tool status, close progress indicators, display results or errors,
	// and track tool execution metrics. Every ToolStart event eventually produces a ToolEnd.
	ToolEnd struct {
		Base
		// ServerData carries server-only metadata emitted alongside the tool result.
		// It is not part of ToolEndPayload and is never serialized into the event
		// payload. Sinks that support server-only sidecars (for example Pulse)
		// may forward it out-of-band for UIs and persistence layers.
		ServerData json.RawMessage `json:"-"`
		// Data contains the structured result metadata for this tool completion. Clients
		// access this field directly for type-safe field access (e.g., event.Data.Duration,
		// event.Data.ToolCallID).
		Data ToolEndPayload
	}

	// Usage reports token usage for a model invocation.
	Usage struct {
		Base
		Data UsagePayload
	}

	// Workflow signals lifecycle phases for a run. Emitted at least once at the end
	// of a run with Phase set to "completed" on success, or "failed"/"canceled"
	// on non-successful terminations.
	Workflow struct {
		Base
		Data WorkflowPayload
	}

	// ChildRunLinked links a parent run/tool call to a spawned child agent run.
	// This allows consumers to attach to child-run streams on demand without
	// flattening child events into the parent.
	ChildRunLinked struct {
		Base
		Data ChildRunLinkedPayload
	}

	// SessionStreamStarted is emitted when a session-scoped stream is created and
	// ready to accept events. It exists to materialize the underlying stream so
	// consumers can subscribe immediately without racing stream creation.
	SessionStreamStarted struct {
		Base
		Data SessionStreamStartedPayload
	}

	// SessionStreamStartedPayload is the typed wire payload for SessionStreamStarted.
	// It is intentionally empty: SessionID is carried on the envelope/Base.
	SessionStreamStartedPayload struct{}

	// SessionStreamEnd is emitted when a session-scoped stream has ended. After this
	// event, no further events are expected to appear in the session stream.
	SessionStreamEnd struct {
		Base
		Data SessionStreamEndPayload
	}

	// SessionStreamEndPayload is the typed wire payload for SessionStreamEnd.
	// It is intentionally empty: SessionID is carried on the envelope/Base.
	SessionStreamEndPayload struct{}

	// RunStreamEnd is an explicit stream boundary marker for a run.
	//
	// Contract:
	// - For a given run, RunStreamEnd must be emitted after all stream-visible events
	//   for that run (notably tool_end events).
	// - Consumers use this marker to terminate stream consumption for a run without
	//   relying on timers or workflow-engine status signals.
	RunStreamEnd struct {
		Base
		Data RunStreamEndPayload
	}

	// RunStreamEndPayload is the typed wire payload for RunStreamEnd.
	// It is intentionally empty: RunID and SessionID are carried on the envelope/Base.
	RunStreamEndPayload struct{}

	// UsagePayload describes token usage details with model attribution.
	UsagePayload struct {
		// TokenUsage contains the attributed token counts reported by the model
		// adapter. Model and ModelClass identify the specific model that produced
		// this delta.
		model.TokenUsage
	}

	// AssistantReplyPayload is the typed wire payload for assistant reply events.
	// It mirrors AssistantReply.Text for consumers decoding Base.Payload().
	AssistantReplyPayload struct {
		Text string `json:"text"`
	}

	// PlannerThoughtPayload is the typed wire payload for planner thought events.
	// Back-compat: Note carries legacy text-only thoughts. When the planner emits
	// structured thinking blocks, Text/Signature or Redacted are populated with
	// ContentIndex and Final flags mirroring provider content blocks.
	PlannerThoughtPayload struct {
		Note         string `json:"note,omitempty"`
		Text         string `json:"text,omitempty"`
		Signature    string `json:"signature,omitempty"`
		Redacted     []byte `json:"redacted,omitempty"`
		ContentIndex int    `json:"content_index,omitempty"`
		Final        bool   `json:"final,omitempty"`
	}

	// AwaitClarification streams a human clarification request from the planner/runtime.
	AwaitClarification struct {
		Base
		Data AwaitClarificationPayload
	}

	// AwaitConfirmation streams an operator confirmation request from the runtime.
	AwaitConfirmation struct {
		Base
		Data AwaitConfirmationPayload
	}

	// AwaitQuestions streams a structured multiple-choice prompt that must be
	// answered out-of-band before the run can resume.
	AwaitQuestions struct {
		Base
		Data AwaitQuestionsPayload
	}

	// AwaitExternalTools streams a request for external tool execution.
	AwaitExternalTools struct {
		Base
		Data AwaitExternalToolsPayload
	}

	// ToolAuthorization streams an operator authorization decision (approve/deny)
	// for a pending tool call. It is emitted when the runtime receives the decision,
	// before tool execution begins (if approved).
	ToolAuthorization struct {
		Base
		Data ToolAuthorizationPayload
	}

	// ToolAuthorizationPayload describes an operator authorization decision.
	ToolAuthorizationPayload struct {
		// ToolName identifies the tool that was authorized.
		ToolName string `json:"tool_name"`
		// ToolCallID is the tool_call_id for the pending tool call.
		ToolCallID string `json:"tool_call_id"`
		// Approved reports whether the operator approved execution.
		Approved bool `json:"approved"`
		// Summary is a deterministic, human-facing description of what was approved.
		Summary string `json:"summary"`
		// ApprovedBy identifies the actor that provided the decision, formatted as
		// "<principal_type>:<principal_id>".
		ApprovedBy string `json:"approved_by"`
	}

	// ToolStartPayload carries the metadata for a scheduled tool invocation. This
	// structure is JSON-serialized when sent over the wire (SSE, WebSocket, Pulse).
	ToolStartPayload struct {
		// ToolCallID uniquely identifies this tool invocation. Clients use this to
		// correlate subsequent ToolEnd events with the original ToolStart, enabling
		// UIs to update progress indicators and display results for the correct tool call.
		ToolCallID string `json:"tool_call_id"`
		// ToolName is the fully qualified tool identifier (e.g., "weather.search.forecast").
		// Format: <service>.<toolset>.<tool>. Clients can use this to display tool names
		// or icons in progress indicators.
		ToolName string `json:"tool_name"`
		// Payload contains the structured tool arguments (JSON-serializable) for this call.
		// It is the canonical tool payload JSON produced by the tool payload codec.
		// It is never decoded into Go structs for streaming to avoid schema drift
		// from untagged Go fields.
		Payload json.RawMessage `json:"payload,omitempty"`
		// DisplayHint is a human-facing one-line description of the in-flight tool work,
		// rendered from DSL-authored templates when available. Suitable for progress lanes
		// and tool ribbons (for example, "Listing devices of kind VAV").
		DisplayHint string `json:"display_hint,omitempty"`
		// Queue is the activity queue name where the tool execution is scheduled. Empty for
		// in-process tools. Clients typically don't display this but may use it for routing
		// or infrastructure-level monitoring.
		Queue string `json:"queue,omitempty"`
		// ParentToolCallID identifies the parent tool that requested this tool, if any.
		// Empty for top-level planner-requested tools. Non-empty when an agent-as-tool
		// schedules child tools. Clients use this to render tool call hierarchies and
		// track parent-child relationships.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
		// ExpectedChildrenTotal indicates how many child tools are expected from this
		// tool's execution batch. Zero means no children are expected or the count is
		// not yet known. Clients use this to display progress like "3 of 5 child tools complete".
		ExpectedChildrenTotal int `json:"expected_children_total,omitempty"`
		// Extra carries optional extension data for clients that need to attach
		// transport- or domain-specific fields without breaking the wire contract.
		// The runtime ignores its contents; sinks may include it when present.
		Extra map[string]any `json:"extra,omitempty"`
	}

	// ToolEndPayload carries the result metadata for a completed tool invocation.
	// This structure is JSON-serialized when sent over the wire (SSE, WebSocket, Pulse).
	ToolEndPayload struct {
		// ToolCallID uniquely identifies the tool invocation that completed. Clients use this
		// to correlate with the original ToolStart event, enabling UIs to match completion
		// events with their corresponding progress indicators and display results in the
		// correct context.
		ToolCallID string `json:"tool_call_id"`
		// ParentToolCallID identifies the parent tool that requested this tool, if any.
		// Empty for top-level planner-requested tools. Matches the ParentToolCallID from
		// the corresponding ToolStart event. Clients use this to maintain tool call hierarchies
		// and track which parent-child relationships have completed.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
		// ToolName is the fully qualified tool identifier that was executed (e.g.,
		// "weather.search.forecast"). Matches the ToolName from ToolStart. Useful for
		// displaying tool names in result summaries and correlating with tool metadata.
		ToolName string `json:"tool_name"`
		// Result contains the tool's output payload. This is the structured data
		// returned by the tool on success. It is the canonical JSON encoding
		// produced by the tool result codec. Nil when the tool failed.
		Result json.RawMessage `json:"result,omitempty"`
		// ResultPreview is a concise, user-facing summary of the tool result rendered from
		// DSL-authored templates when available. It is intended for UI ribbons and summaries
		// (for example, "Device list ready" or "Found 3 critical alarms").
		ResultPreview string `json:"result_preview,omitempty"`
		// Bounds, when non-nil, describes how the tool result has been bounded
		// relative to the full underlying data set (for example, list/window/
		// graph caps). It is supplied by tool implementations and surfaced for
		// observability; the runtime does not modify it.
		Bounds *agent.Bounds `json:"bounds,omitempty"`
		// Duration is the wall-clock execution time for the tool activity, including any
		// queuing delay, retries, and processing time. Clients can display this in
		// performance dashboards or debug panels to identify slow tools.
		Duration time.Duration `json:"duration"`
		// Telemetry holds structured observability metadata collected during tool execution:
		// token counts, model identifiers, retry attempts, and provider-specific metrics.
		// Nil if no telemetry was collected. Clients use this for cost tracking, performance
		// monitoring, and compliance reporting.
		Telemetry *telemetry.ToolTelemetry `json:"telemetry,omitempty"`
		// RetryHint carries structured guidance for recovering from tool failures.
		// When present, clients can ask the user a clarifying question and retry
		// the tool call deterministically.
		RetryHint *planner.RetryHint `json:"retry_hint,omitempty"`
		// Error contains any error returned by the tool execution. Nil on success. When
		// non-nil, Result is nil and this field contains structured error details (code,
		// message, retryability). Clients display error messages and may implement retry UIs.
		Error *toolerrors.ToolError `json:"error,omitempty"`
		// Extra carries optional extension data for clients that need to attach
		// transport- or domain-specific fields without breaking the wire contract.
		// The runtime ignores its contents; sinks may include it when present.
		Extra map[string]any `json:"extra,omitempty"`
	}

	// RunLinkPayload describes a link to a nested agent run for streaming.
	RunLinkPayload struct {
		// RunID is the workflow execution identifier of the child run.
		RunID string `json:"run_id"`
		// AgentID is the identifier of the child agent that executed
		// the nested run.
		AgentID agent.Ident `json:"agent_id"`
		// ParentRunID is the run identifier of the parent workflow
		// that launched this child run. It may be empty when the
		// child has no recorded parent.
		ParentRunID string `json:"parent_run_id,omitempty"`
		// ParentToolCallID is the tool call identifier on the parent
		// run that triggered this child run. It may be empty when the
		// linkage is not available.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	}

	// AwaitClarificationPayload describes a human clarification request.
	AwaitClarificationPayload struct {
		ID             string         `json:"id"`
		Question       string         `json:"question"`
		MissingFields  []string       `json:"missing_fields,omitempty"`
		RestrictToTool string         `json:"restrict_to_tool,omitempty"`
		ExampleInput   map[string]any `json:"example_input,omitempty"`
	}

	// AwaitConfirmationPayload describes an operator confirmation request.
	AwaitConfirmationPayload struct {
		// ID correlates this await with a subsequent confirmation decision.
		ID string `json:"id"`
		// Title is an optional display title for the confirmation UI.
		Title string `json:"title,omitempty"`
		// Prompt is the operator-facing confirmation prompt.
		Prompt string `json:"prompt"`
		// ToolName identifies the tool that requires confirmation.
		ToolName string `json:"tool_name"`
		// ToolCallID is the tool_call_id for the pending tool call.
		ToolCallID string `json:"tool_call_id"`
		// Payload contains the canonical JSON arguments for the pending tool call.
		Payload json.RawMessage `json:"payload,omitempty"`
	}

	// AwaitQuestionsPayload describes a structured multiple-choice prompt that must
	// be answered out-of-band (typically by a UI) and resumed via ProvideToolResults.
	AwaitQuestionsPayload struct {
		// ID correlates this await with a subsequent provide_tool_results call.
		ID string `json:"id"`
		// ToolName identifies the tool awaiting user answers.
		ToolName string `json:"tool_name"`
		// ToolCallID correlates the provided result with this requested call.
		ToolCallID string `json:"tool_call_id"`
		// Title is an optional display title for the questions UI.
		Title *string `json:"title,omitempty"`
		// Questions are the structured questions to present to the user.
		Questions []AwaitQuestionPayload `json:"questions"`
	}

	// AwaitQuestionPayload describes a single multiple-choice question.
	AwaitQuestionPayload struct {
		ID            string                       `json:"id"`
		Prompt        string                       `json:"prompt"`
		Options       []AwaitQuestionOptionPayload `json:"options"`
		AllowMultiple bool                         `json:"allow_multiple,omitempty"`
	}

	// AwaitQuestionOptionPayload describes a selectable answer option.
	AwaitQuestionOptionPayload struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}

	// AwaitExternalToolsPayload describes external tool requests to be provided by callers.
	AwaitExternalToolsPayload struct {
		// ID correlates this await with a subsequent provide_tool_results
		// call from the orchestrator or UI.
		ID string `json:"id"`
		// Items enumerates the external tool calls that must be satisfied
		// before the run can resume.
		Items []AwaitToolPayload `json:"items"`
	}

	// AwaitToolPayload describes a single external tool call to be satisfied.
	AwaitToolPayload struct {
		// ToolName is the fully qualified identifier of the external tool
		// that must be executed (for example, "atlas.read.get_time_series").
		ToolName string `json:"tool_name"`
		// ToolCallID optionally carries a caller-assigned identifier used
		// to correlate the provided result with this request.
		ToolCallID string `json:"tool_call_id,omitempty"`
		// Payload contains the JSON-serializable arguments for the external
		// tool. It may be omitted when the tool takes no parameters.
		Payload json.RawMessage `json:"payload,omitempty"`
	}

	// ToolUpdatePayload describes a non-terminal update to a tool call, typically used
	// when a parent tool dynamically discovers more child tools across planning iterations.
	ToolUpdatePayload struct {
		// ToolCallID identifies the (parent) tool call being updated.
		ToolCallID string `json:"tool_call_id"`
		// ExpectedChildrenTotal is the new total of expected child tools.
		ExpectedChildrenTotal int `json:"expected_children_total"`
	}

	// ToolCallArgsDeltaPayload describes a streamed tool-call argument fragment.
	ToolCallArgsDeltaPayload struct {
		// ToolCallID identifies the tool call being streamed.
		ToolCallID string `json:"tool_call_id"`
		// ToolName is the canonical tool identifier for this delta stream.
		ToolName string `json:"tool_name"`
		// Delta is the raw tool input JSON fragment emitted by the provider.
		Delta string `json:"delta"`
	}

	// ToolOutputDeltaPayload describes a streamed tool output fragment.
	ToolOutputDeltaPayload struct {
		// ToolCallID identifies the tool call producing the output.
		ToolCallID string `json:"tool_call_id"`
		// ParentToolCallID optionally identifies the parent tool call when the tool
		// was invoked as part of an agent-as-tool run.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
		// ToolName is the canonical tool identifier for this delta stream.
		ToolName string `json:"tool_name"`
		// Stream identifies which logical output channel produced the delta
		// (for example, "stdout", "stderr", "log", "progress").
		Stream string `json:"stream"`
		// Delta is the raw output fragment emitted by the tool.
		Delta string `json:"delta"`
	}

	// Base provides a default implementation of Event. Embed this struct in concrete
	// event types to inherit the Type(), RunID(), SessionID(), and Payload() methods.
	// All stream event types (AssistantReply, ToolStart, etc.) embed Base to avoid
	// boilerplate.
	//
	// Field names are abbreviated to minimize visual clutter when constructing events,
	// since Base fields are rarely accessed directly (consumers use the interface methods
	// or type-assert to concrete types for their specific fields).
	Base struct {
		// t is the event type constant (e.g., EventToolEnd, EventAssistantReply).
		// Set this when constructing concrete events to identify the payload category.
		t EventType
		// r is the workflow run identifier that produced this event. All events from
		// a single run share the same R value, enabling clients to filter or correlate
		// events by run.
		r string
		// s is the logical session identifier for the run that produced this event.
		// All events from a single run share the same S value, enabling subscribers
		// to join streams to session-scoped stores without out-of-band registries.
		s string
		// p is the JSON-serializable payload returned by the Payload() method. Sinks
		// marshal this value when publishing events. Set P to the appropriate payload
		// type for the event (e.g., ToolStartPayload for ToolStart events).
		p any
	}

	// WorkflowPayload describes a run lifecycle update.
	WorkflowPayload struct {
		// Name is an optional human-readable workflow name.
		Name string `json:"name,omitempty"`
		// Phase is the lifecycle phase, e.g., "completed", "failed", "canceled".
		Phase string `json:"phase"`
		// Status is the coarse-grained terminal status when known
		// (typically "success", "failed", or "canceled"). It is populated
		// on terminal updates derived from RunCompletedEvent and may be
		// empty for non-terminal phase transitions.
		Status string `json:"status,omitempty"`
		// Error is a user-safe error message intended to be displayed directly
		// to end users. It is populated only on failures and is empty on success
		// and cancellations.
		Error string `json:"error,omitempty"`
		// DebugError is a raw error string intended for logs and diagnostics. It
		// may contain infrastructure details and should not be rendered in UIs.
		DebugError string `json:"debug_error,omitempty"`
		// ErrorProvider identifies the model provider when the terminal error was
		// caused by a provider failure (for example, "bedrock").
		ErrorProvider string `json:"error_provider,omitempty"`
		// ErrorOperation identifies the provider operation when available.
		ErrorOperation string `json:"error_operation,omitempty"`
		// ErrorKind classifies provider failures into a small set of stable categories.
		ErrorKind string `json:"error_kind,omitempty"`
		// ErrorCode is the provider-specific error code when available.
		ErrorCode string `json:"error_code,omitempty"`
		// HTTPStatus is the provider HTTP status code when available.
		HTTPStatus int `json:"http_status,omitempty"`
		// Retryable reports whether retrying may succeed without changing the request.
		Retryable bool `json:"retryable"`
	}

	// ChildRunLinkedPayload describes an agent-as-tool child run link.
	ChildRunLinkedPayload struct {
		// ToolName is the fully qualified identifier of the parent tool
		// that launched the child agent run.
		ToolName string `json:"tool_name"`
		// ToolCallID identifies the parent tool call associated with the
		// child run. UIs use this to attach child run links to tool cards.
		ToolCallID string `json:"tool_call_id"`
		// ChildRunID is the workflow execution identifier of the nested
		// agent run.
		ChildRunID string `json:"child_run_id"`
		// ChildAgentID is the identifier of the agent that executed the
		// child run.
		ChildAgentID agent.Ident `json:"child_agent_id"`
	}

	// StreamProfile describes which event kinds are emitted for a particular
	// audience. Profiles are applied by the Subscriber when mapping hook events
	// → stream events.
	StreamProfile struct {
		// Assistant controls assistant reply emission.
		Assistant bool
		// Thoughts controls planner thought / thinking emission.
		Thoughts bool
		// ToolStart controls emission of tool_start events.
		ToolStart bool
		// ToolUpdate controls emission of tool_update events.
		ToolUpdate bool
		// ToolCallArgsDelta controls emission of tool_call_args_delta events.
		ToolCallArgsDelta bool
		// ToolEnd controls emission of tool_end events.
		ToolEnd bool
		// AwaitClarification controls emission of await_clarification events.
		AwaitClarification bool
		// AwaitConfirmation controls emission of await_confirmation events.
		AwaitConfirmation bool
		// AwaitQuestions controls emission of await_questions events.
		AwaitQuestions bool
		// AwaitExternalTools controls emission of await_external_tools events.
		AwaitExternalTools bool
		// ToolAuthorization controls emission of tool_authorization events.
		ToolAuthorization bool
		// Usage controls emission of usage events.
		Usage bool
		// Workflow controls emission of workflow lifecycle events.
		Workflow bool
		// ChildRuns controls emission of child_run_linked events.
		ChildRuns bool
	}
)

// DefaultProfile returns a StreamProfile that emits all event kinds and
// links child runs via ChildRunLinked events without flattening them into
// the parent stream.
func DefaultProfile() StreamProfile {
	return StreamProfile{
		Assistant:          true,
		Thoughts:           true,
		ToolStart:          true,
		ToolUpdate:         true,
		ToolCallArgsDelta:  true,
		ToolEnd:            true,
		AwaitClarification: true,
		AwaitConfirmation:  true,
		AwaitQuestions:     true,
		AwaitExternalTools: true,
		ToolAuthorization:  true,
		Usage:              true,
		Workflow:           true,
		ChildRuns:          true,
	}
}

// UserChatProfile returns a profile suitable for end-user chat views. It emits
// assistant replies, tool start/end/update, awaits, usage, workflow, and
// child_run_linked links, and keeps child runs on their own streams so UIs
// can attach on demand.
func UserChatProfile() StreamProfile {
	return DefaultProfile()
}

// AgentDebugProfile returns a verbose profile intended for operational and
// debugging views. Child runs are linked via ChildRunLinked events and are not
// flattened into the parent stream.
func AgentDebugProfile() StreamProfile {
	return DefaultProfile()
}

// MetricsProfile returns a profile that emits only usage and workflow events,
// suitable for metrics/telemetry pipelines.
func MetricsProfile() StreamProfile {
	return StreamProfile{
		Usage:    true,
		Workflow: true,
	}
}

// EventType enumerates stream payload flavors.
type EventType string

const (
	// EventPlannerThought streams incremental planner reasoning and annotations during
	// execution. These events allow clients to display "thinking..." indicators and show
	// intermediate planner thoughts before tool calls complete. Emitted by StreamSubscriber
	// when PlannerNoteEvent hooks fire. The payload contains the planner's text annotation.
	EventPlannerThought EventType = "planner_thought"

	// EventToolStart streams when a tool activity is scheduled for execution. Clients
	// receive this before the tool executes, allowing UIs to display pending tool calls,
	// show progress indicators, and track parent-child tool relationships for agent-as-tool
	// batches. Emitted by StreamSubscriber when ToolCallScheduledEvent hooks fire.
	EventToolStart EventType = "tool_start"

	// EventToolEnd streams when a tool activity completes with either a result or error.
	// This event includes execution duration, telemetry (token counts, model info), and
	// structured error details if the tool failed. UIs use this to update tool status,
	// display results, and close progress indicators. Emitted by StreamSubscriber when
	// ToolResultReceivedEvent hooks fire.
	EventToolEnd EventType = "tool_end"

	// EventToolUpdate streams a non-terminal update to a tool call (e.g., when a parent
	// tool discovers additional child tools to execute). Emitted by StreamSubscriber when
	// ToolCallUpdatedEvent hooks fire. The payload carries the updated expected child
	// count for progress tracking.
	EventToolUpdate EventType = "tool_update"

	// EventToolCallArgsDelta streams an incremental tool-call argument fragment as
	// the model provider streams tool input JSON.
	//
	// Naming note: this is an args *delta* (not a tool call). Fragments are not
	// guaranteed to be valid JSON boundaries and must not be treated as canonical.
	// Consumers may ignore these events; the canonical tool payload is still
	// emitted via EventToolStart (tool_start) and the final tool call/tool_end
	// events.
	EventToolCallArgsDelta EventType = "tool_call_args_delta"

	// EventToolOutputDelta streams an incremental tool output fragment while the
	// tool is running.
	EventToolOutputDelta EventType = "tool_output_delta"

	// EventAssistantReply streams incremental assistant message content as the planner
	// produces the final response. Clients receive text chunks that can be displayed
	// progressively (streaming typewriter effect). Emitted by StreamSubscriber when
	// AssistantMessageEvent hooks fire. Payload is AssistantReplyPayload.
	EventAssistantReply EventType = "assistant_reply"

	// EventAwaitClarification streams when a planner requests human clarification.
	EventAwaitClarification EventType = "await_clarification"

	// EventAwaitConfirmation streams when the runtime requests operator confirmation.
	EventAwaitConfirmation EventType = "await_confirmation"

	// EventAwaitQuestions streams when a planner requests structured multiple-choice input.
	EventAwaitQuestions EventType = "await_questions"

	// EventAwaitExternalTools streams when a planner requests external tool execution.
	EventAwaitExternalTools EventType = "await_external_tools"

	// EventToolAuthorization streams when an operator provides an explicit
	// approval/denial decision for a pending tool call.
	EventToolAuthorization EventType = "tool_authorization"

	// EventUsage streams token usage details.
	EventUsage EventType = "usage"

	// EventWorkflow streams lifecycle phases for the run (e.g., completed).
	EventWorkflow EventType = "workflow"

	// EventChildRunLinked links a parent tool call to a spawned child agent run.
	EventChildRunLinked EventType = "child_run_linked"

	// EventSessionStreamStarted marks that a session stream has been created.
	EventSessionStreamStarted EventType = "session_stream_started"

	// EventSessionStreamEnd marks that a session stream has ended.
	EventSessionStreamEnd EventType = "session_stream_end"

	// EventRunStreamEnd marks the end of stream-visible events for a run.
	EventRunStreamEnd EventType = "run_stream_end"
)

// NewBase constructs a Base event with the given type, run ID, optional
// session ID, and payload.
func NewBase(t EventType, runID, sessionID string, payload any) Base {
	return Base{t: t, r: runID, s: sessionID, p: payload}
}

// Type implements Event.Type.
func (e Base) Type() EventType { return e.t }

// RunID implements Event.RunID.
func (e Base) RunID() string { return e.r }

// SessionID implements Event.SessionID.
func (e Base) SessionID() string { return e.s }

// Payload implements Event.Payload.
func (e Base) Payload() any { return e.p }
