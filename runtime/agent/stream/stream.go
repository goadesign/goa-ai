// Package stream provides typed real-time agent execution updates as private
// input to a trusted runtime host. Stream events differ from hook events:
// stream events describe assistant output and run progress, while hook events
// provide comprehensive internal observability across the runtime lifecycle.
// Stream events are never safe to forward unchanged to a browser or another
// end-user client. Some include exact provider messages and other private
// runtime data. The host must select and convert the data it exposes through
// its own smaller public contract.
//
// Subscriber converts selected persisted hook events into stream events and
// also applies the same purpose-specific profile to live model text and
// thinking events.
// Internal-only events such as policy decisions and memory operations never
// reach the sink.
//
// All event types implement Event and can be sent concurrently through a Sink.
// A sink may encode events for a private transport between the runtime and its
// trusted host. That encoding does not make the events safe for end users.
package stream

import (
	"context"
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	// Sink delivers private runtime updates to a trusted host. The host owns any
	// public event contract built from these updates and must not forward the
	// runtime events unchanged to end users.
	// Implementations must be thread-safe: the runtime may call Send concurrently from
	// multiple goroutines when streaming tool results or planner thoughts in parallel.
	//
	// Naming note: Send belongs to the sink (the transmitter), not the subscriber.
	// The hooks.StreamSubscriber RECEIVES events from the internal bus and forwards
	// them by invoking Sink.Send. Transports and tests implement Sink; typical
	// application code does not call Send directly unless it is acting as a transport.
	Sink interface {
		// Send publishes an event to the sink's private transport. The
		// implementation encodes the event and handles delivery details such as
		// retry, buffering, and backpressure.
		// When EventKey is non-empty, exact retries carry the same event body and
		// Send must return the original publication instead of creating a duplicate.
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

	// Event describes private runtime data delivered to a trusted host through a
	// Sink. All concrete event types embed Base to provide standard metadata such
	// as type, run ID, and payload. Sinks use Event to encode values generically;
	// trusted host code can type-assert to concrete types for structured access.
	//
	// Implementations are immutable after construction and safe to send concurrently.
	Event interface {
		// Type returns the event type constant (e.g., EventToolEnd, EventAssistantReply).
		// Subscribers use this to filter events by category or route to
		// type-specific handlers without performing type assertions.
		Type() EventType

		// RunID returns the unique workflow run identifier that produced this event. All
		// events within a single run execution share the same run ID, enabling a
		// trusted host to filter or group events when one Sink carries multiple runs.
		RunID() string

		// SessionID returns the logical session identifier associated with the run.
		// All events for a given run share the same session ID, providing a stable
		// join key across processes and transports.
		SessionID() string

		// EventKey returns the stable logical identity of the originating hook event
		// when the stream event was derived from one. Empty means the producer did not
		// attach a durable identity.
		EventKey() string

		// OccurredAt returns the stable source timestamp for keyed events. Events
		// without a durable key may return the zero time and let the sink timestamp
		// their one-time publication.
		OccurredAt() time.Time

		// Payload returns the event-specific data in a JSON-serializable form. Sinks use
		// this for generic marshaling when they don't need typed access. For example, the
		// Pulse sink calls Payload() and marshals the result to JSON without knowing the
		// concrete event type.
		//
		// Trusted host code that needs structured fields should type-assert the Event
		// and access values such as ToolStart.Data directly. Use Payload for private
		// generic encoding and type assertions for type-safe field access.
		Payload() any
	}

	// AssistantReply streams incremental assistant message content as the planner
	// receives provider text chunks. Once emitted, text is append-only; later
	// validation of tool calls cannot retract it.
	AssistantReply struct {
		Base
		// Data contains one text fragment and the model response it belongs to.
		Data AssistantReplyPayload
	}

	// AssistantTurn streams the complete messages for one assistant response
	// after the runtime has durably appended them. The run may still fail after
	// its text fragments were published to the trusted host.
	//
	// Contract:
	//   - ResponseID matches every AssistantReply fragment in this response.
	//   - Messages contain the exact ordered assistant transcript, including
	//     provider-only metadata and structured parts. They must not be sent to
	//     an end-user client unchanged.
	//   - When fragments exist, their ordered text is an exact prefix of the text
	//     in Messages, so consumers can append only a missing suffix without
	//     replacing live text.
	AssistantTurn struct {
		Base
		Data AssistantTurnPayload
	}

	// PlannerThought carries live planner notes or provider thinking to a trusted
	// host. The host decides which fields belong in its public event contract;
	// this private event must not be forwarded to an end-user client unchanged.
	PlannerThought struct {
		Base
		// Data contains planner notes or provider thinking.
		Data PlannerThoughtPayload
	}

	// PromptRendered reports a rendered prompt reference and scope used by the
	// runtime for prompt resolution.
	PromptRendered struct {
		Base
		Data PromptRenderedPayload
	}

	// ToolStart reports that the runtime scheduled a tool activity. A trusted host
	// receives it before execution and may derive public progress from selected fields.
	ToolStart struct {
		Base
		// Data contains the structured metadata for this tool invocation.
		Data ToolStartPayload
	}

	// ToolUpdate streams progress updates for a tool call (new expected child count).
	ToolUpdate struct {
		Base
		Data ToolUpdatePayload
	}

	// ToolOutputDelta streams an incremental tool output fragment while the tool
	// is still running.
	//
	// Contract:
	//   - This is a best-effort progress event. Hosts may ignore it entirely.
	//   - The canonical tool output is still emitted via ToolEnd.
	ToolOutputDelta struct {
		Base
		Data ToolOutputDeltaPayload
	}

	// ToolEnd reports that a tool activity completed with either a result or an
	// error. Every ToolStart event eventually produces a ToolEnd.
	ToolEnd struct {
		Base
		// ServerData carries server-only metadata emitted alongside the tool result.
		// It is not part of ToolEndPayload and is never serialized into the event
		// payload. Sinks that support server-only data (for example Pulse) may
		// preserve it for host persistence or an application-owned public event.
		ServerData rawjson.Message `json:"-"`
		// Data contains the structured result metadata for this tool completion.
		Data ToolEndPayload
	}

	// Usage reports token usage for a model invocation.
	Usage struct {
		Base
		Data UsagePayload
	}

	// Workflow reports lifecycle phases for a run. Emitted at least once at the end
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

	// SessionStreamStartedPayload is the typed payload for SessionStreamStarted.
	// It is intentionally empty: SessionID is carried on the envelope/Base.
	SessionStreamStartedPayload struct{}

	// SessionStreamEnd is emitted when a session-scoped stream has ended. After this
	// event, no further events are expected to appear in the session stream.
	SessionStreamEnd struct {
		Base
		Data SessionStreamEndPayload
	}

	// SessionStreamEndPayload is the typed payload for SessionStreamEnd.
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

	// RunStreamEndPayload is the typed payload for RunStreamEnd.
	// It is intentionally empty: RunID and SessionID are carried on the envelope/Base.
	RunStreamEndPayload struct{}

	// UsagePayload describes token usage details with model attribution.
	UsagePayload struct {
		// TokenUsage contains the attributed token counts reported by the model
		// adapter. Model and ModelClass identify the specific model that produced
		// this delta.
		model.TokenUsage
	}

	// AssistantReplyPayload is the typed payload for assistant reply events.
	// ResponseID binds the fragment to the exact planner activity that produced
	// it so a trusted host can group separate responses without guessing from timing.
	AssistantReplyPayload struct {
		ResponseID string `json:"response_id"`
		Text       string `json:"text"`
	}

	// AssistantTurnPayload carries one exact committed assistant response for a
	// trusted host. Messages may contain provider-only data.
	AssistantTurnPayload struct {
		ResponseID string           `json:"response_id"`
		Messages   []*model.Message `json:"messages"`
	}

	// PlannerThoughtPayload is the typed payload for live thought events.
	// Note carries planner notes and non-final provider thinking.
	// Structured thinking blocks also populate Text/Signature or Redacted with
	// ContentIndex and Final flags matching the provider content blocks.
	PlannerThoughtPayload struct {
		ResponseID   string `json:"response_id,omitempty"`
		Note         string `json:"note,omitempty"`
		Text         string `json:"text,omitempty"`
		Signature    string `json:"signature,omitempty"`
		Redacted     []byte `json:"redacted,omitempty"`
		ContentIndex int    `json:"content_index,omitempty"`
		Final        bool   `json:"final,omitempty"`
	}

	// PromptRenderedPayload describes one rendered prompt reference and scope.
	PromptRenderedPayload struct {
		PromptID string       `json:"prompt_id"`
		Version  string       `json:"version"`
		Scope    prompt.Scope `json:"scope"`
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
	// answered before a successor run can continue the work.
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

	// ToolStartPayload carries private metadata for a scheduled tool invocation.
	ToolStartPayload struct {
		// ToolCallID uniquely identifies this tool invocation and correlates the
		// matching ToolEnd event.
		ToolCallID string `json:"tool_call_id"`
		// ToolName is the fully qualified tool identifier (e.g., "weather.search.forecast").
		// Format: <service>.<toolset>.<tool>.
		ToolName string `json:"tool_name"`
		// Payload contains the structured tool arguments (JSON-serializable) for this call.
		// It is the canonical tool payload JSON produced by the tool payload codec.
		// It is never decoded into Go structs for streaming to avoid schema drift
		// from untagged Go fields.
		Payload rawjson.Message `json:"payload,omitempty"`
		// DisplayHint is a one-line description of the in-flight tool work,
		// rendered from a DSL-authored template when available. A host may use it
		// when building its public progress event.
		DisplayHint string `json:"display_hint,omitempty"`
		// Queue is the activity queue name where the tool execution is scheduled. Empty for
		// in-process tools. It is private scheduling data.
		Queue string `json:"queue,omitempty"`
		// ParentToolCallID identifies the parent tool that requested this tool, if any.
		// Empty for top-level planner-requested tools. Non-empty when an agent-as-tool
		// schedules child tools. It lets the host track parent-child relationships.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
		// ExpectedChildrenTotal indicates how many child tools are expected from this
		// tool's execution batch. Zero means no children are expected or the count is
		// not yet known.
		ExpectedChildrenTotal int `json:"expected_children_total,omitempty"`
		// Extra carries optional extension data that trusted hosts can attach
		// without changing the core payload.
		// The runtime ignores its contents; sinks may include it when present.
		Extra map[string]any `json:"extra,omitempty"`
	}

	// ToolEndPayload carries private result metadata for a completed tool invocation.
	ToolEndPayload struct {
		// CallRunID identifies the workflow run whose ToolStart event opened this
		// tool invocation. It differs from the enclosing event's run ID when a
		// continuation workflow supplies an externally produced result.
		CallRunID string `json:"call_run_id"`
		// ToolCallID uniquely identifies the tool invocation that completed and
		// correlates it with the original ToolStart event.
		ToolCallID string `json:"tool_call_id"`
		// ParentToolCallID identifies the parent tool that requested this tool, if any.
		// Empty for top-level planner-requested tools. Matches the ParentToolCallID from
		// the corresponding ToolStart event.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
		// ToolName is the fully qualified tool identifier that was executed (e.g.,
		// "weather.search.forecast"). It matches ToolName from ToolStart.
		ToolName string `json:"tool_name"`
		// Result contains the tool's output payload. This is the structured data
		// returned by the tool on success. It is the canonical JSON encoding
		// produced by the tool result codec. Nil when the tool failed or when the
		// tool does not define a result.
		Result rawjson.Message `json:"result,omitempty"`
		// ResultPreview is a concise summary of the tool result rendered from a
		// DSL-authored template when available. A host may use it when building a
		// public result summary.
		ResultPreview string `json:"result_preview,omitempty"`
		// Bounds, when non-nil, describes how the tool result has been bounded
		// relative to the full underlying data set (for example, list/window/
		// graph caps). It is supplied by tool implementations and surfaced for
		// observability; the runtime does not modify it.
		Bounds *agent.Bounds `json:"bounds,omitempty"`
		// Duration is the wall-clock execution time for the tool activity, including
		// queuing delay, retries, and processing time.
		Duration time.Duration `json:"duration"`
		// Telemetry holds structured observability metadata collected during tool execution:
		// token counts, model identifiers, retry attempts, and provider-specific metrics.
		// Nil if no telemetry was collected.
		Telemetry *telemetry.ToolTelemetry `json:"telemetry,omitempty"`
		// Failure contains the stable failure classification and recovery action.
		// Nil on success.
		Failure *planner.ToolFailure `json:"failure,omitempty"`
		// Extra carries optional extension data that trusted hosts can attach
		// without changing the core payload.
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
		ID             string          `json:"id"`
		Question       string          `json:"question"`
		MissingFields  []string        `json:"missing_fields,omitempty"`
		RestrictToTool string          `json:"restrict_to_tool,omitempty"`
		ExampleJSON    rawjson.Message `json:"example_json,omitempty"`
	}

	// AwaitConfirmationPayload describes an operator confirmation request.
	AwaitConfirmationPayload struct {
		// ID correlates this await with a subsequent confirmation decision.
		ID string `json:"id"`
		// Title is an optional title for a host-created confirmation prompt.
		Title string `json:"title,omitempty"`
		// Prompt is the operator-facing confirmation prompt.
		Prompt string `json:"prompt"`
		// ToolName identifies the tool that requires confirmation.
		ToolName string `json:"tool_name"`
		// ToolCallID is the tool_call_id for the pending tool call.
		ToolCallID string `json:"tool_call_id"`
		// Payload contains the canonical JSON arguments for the pending tool call.
		Payload rawjson.Message `json:"payload,omitempty"`
	}

	// AwaitQuestionsPayload describes a structured multiple-choice prompt whose
	// answers are submitted in the next workflow continuation.
	AwaitQuestionsPayload struct {
		// ID correlates this request with the continuation response.
		ID string `json:"id"`
		// ToolName identifies the tool awaiting user answers.
		ToolName string `json:"tool_name"`
		// ToolCallID correlates the provided result with this requested call.
		ToolCallID string `json:"tool_call_id"`
		// Title is an optional title for a host-created question prompt.
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
		// call from the application that owns the external tool.
		ID string `json:"id"`
		// Items enumerates the external tool calls whose results let a successor
		// run continue the work.
		Items []AwaitToolPayload `json:"items"`
	}

	// AwaitToolPayload describes a single external tool call to be satisfied.
	AwaitToolPayload struct {
		// ToolName is the fully qualified identifier of the external tool
		// that must be executed (for example, "svc.read.get_time_series").
		ToolName string `json:"tool_name"`
		// ToolCallID optionally carries a caller-assigned identifier used
		// to correlate the provided result with this request.
		ToolCallID string `json:"tool_call_id,omitempty"`
		// Payload contains the JSON-serializable arguments for the external
		// tool. It may be omitted when the tool takes no parameters.
		Payload rawjson.Message `json:"payload,omitempty"`
	}

	// ToolUpdatePayload describes a non-terminal update to a tool call, typically used
	// when a parent tool dynamically discovers more child tools across planning iterations.
	ToolUpdatePayload struct {
		// ToolCallID identifies the (parent) tool call being updated.
		ToolCallID string `json:"tool_call_id"`
		// ExpectedChildrenTotal is the new total of expected child tools.
		ExpectedChildrenTotal int `json:"expected_children_total"`
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
		// a single run share the same R value, enabling a host to filter or
		// correlate events by run.
		r string
		// s is the logical session identifier for the run that produced this event.
		// All events from a single run share the same S value, enabling subscribers
		// to join streams to session-scoped stores without out-of-band registries.
		s string
		// k is the stable logical identity propagated from the originating hook event.
		k string
		// at is the source event time. Keyed retries preserve it so exact stream
		// publication sees byte-identical envelopes.
		at time.Time
		// p is the JSON-serializable payload returned by the Payload() method. Sinks
		// marshal this value when publishing events. Set P to the appropriate payload
		// type for the event (e.g., ToolStartPayload for ToolStart events).
		p any
	}

	// WorkflowPayload describes a run lifecycle update.
	WorkflowPayload struct {
		// Name is an optional human-readable workflow name.
		Name string `json:"name,omitempty"`
		// Phase is the lifecycle phase, e.g., "completed", "failed", "canceled", "suspended".
		Phase string `json:"phase"`
		// Status is the coarse-grained terminal status when known
		// ("success", "failed", "canceled", or "suspended"). It is populated
		// on terminal updates derived from RunCompletedEvent or RunSuspendedEvent and may be
		// empty for non-terminal phase transitions.
		Status string `json:"status,omitempty"`
		// Failure carries the canonical failure payload for failed terminal updates.
		Failure *run.Failure `json:"failure,omitempty"`
		// Cancellation carries the canonical cancellation payload for canceled
		// terminal updates.
		Cancellation *run.Cancellation `json:"cancellation,omitempty"`
	}

	// ChildRunLinkedPayload describes an agent-as-tool child run link.
	ChildRunLinkedPayload struct {
		// ToolName is the fully qualified identifier of the parent tool
		// that launched the child agent run.
		ToolName string `json:"tool_name"`
		// ToolCallID identifies the parent tool call associated with the child run.
		ToolCallID string `json:"tool_call_id"`
		// ChildRunID is the workflow execution identifier of the nested
		// agent run.
		ChildRunID string `json:"child_run_id"`
		// ChildAgentID is the identifier of the agent that executed the
		// child run.
		ChildAgentID agent.Ident `json:"child_agent_id"`
	}

	// StreamProfile describes which event kinds are emitted for one purpose.
	// Subscriber applies it to mapped hook events and live model output. The
	// tool executor applies ToolOutputDelta when it forwards remote-tool output.
	StreamProfile struct {
		// Assistant controls assistant reply emission.
		Assistant bool
		// AssistantTurns controls emission of exact committed assistant messages.
		AssistantTurns bool
		// Thoughts controls planner-thought and live model-thinking
		// events. It does not remove thinking parts or provider metadata from
		// exact messages emitted through AssistantTurn.
		Thoughts bool
		// PromptRendered controls emission of prompt_rendered events.
		PromptRendered bool
		// ToolStart controls emission of tool_start events.
		ToolStart bool
		// ToolUpdate controls emission of tool_update events.
		ToolUpdate bool
		// ToolOutputDelta controls emission of incremental tool output.
		ToolOutputDelta bool
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

// RuntimeHostProfile returns private input for a trusted runtime host to save,
// replay, present, and process an agent run. It includes live thinking so the
// host can deliberately translate selected fields into its public contract.
// Exact committed AssistantTurn messages still contain all provider data.
// This profile is never browser-safe. A host must select and convert the data
// it exposes through its own smaller public contract.
func RuntimeHostProfile() StreamProfile {
	return StreamProfile{
		Assistant:          true,
		AssistantTurns:     true,
		Thoughts:           true,
		PromptRendered:     true,
		ToolStart:          true,
		ToolUpdate:         true,
		ToolOutputDelta:    true,
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

// AgentDebugProfile returns every private event for restricted operational
// diagnostics. It names the diagnostic purpose at the call site while using
// the same complete event set as a trusted runtime host. These events must not
// be sent to an end-user client unchanged.
func AgentDebugProfile() StreamProfile {
	return RuntimeHostProfile()
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
	// EventPlannerThought carries planner notes or provider thinking to a trusted
	// host. It must not be sent to an end-user client unchanged.
	EventPlannerThought EventType = "planner_thought"

	// EventPromptRendered streams prompt render references and scopes used by runtime prompt resolution.
	EventPromptRendered EventType = "prompt_rendered"

	// EventToolStart reports that a tool activity was scheduled. It is emitted by
	// Subscriber when a ToolCallScheduledEvent hook fires.
	EventToolStart EventType = "tool_start"

	// EventToolEnd streams when a tool activity completes with either a result or error.
	// This event includes execution duration, telemetry (token counts, model info), and
	// structured error details if the tool failed. It is emitted by Subscriber
	// when a ToolResultReceivedEvent hook fires.
	EventToolEnd EventType = "tool_end"

	// EventToolUpdate streams a non-terminal update to a tool call (e.g., when a parent
	// tool discovers additional child tools to execute). Emitted by StreamSubscriber when
	// ToolCallUpdatedEvent hooks fire. The payload carries the updated expected child
	// count for progress tracking.
	EventToolUpdate EventType = "tool_update"

	// EventToolOutputDelta streams an incremental tool output fragment while the
	// tool is running.
	EventToolOutputDelta EventType = "tool_output_delta"

	// EventAssistantReply carries incremental assistant message content as the
	// planner produces a response. The runtime emits these events directly from
	// live model output; completed assistant-message hooks do not recreate them.
	// Payload is AssistantReplyPayload.
	EventAssistantReply EventType = "assistant_reply"

	// EventAssistantTurn streams one exact committed assistant response after the
	// runtime has durably appended its provider transcript to the run log.
	EventAssistantTurn EventType = "assistant_turn"

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

// NewBaseWithEventKey constructs a Base event and attaches the stable logical
// identity and source timestamp of the originating hook event.
func NewBaseWithEventKey(
	t EventType,
	runID, sessionID string,
	payload any,
	eventKey string,
	occurredAt time.Time,
) Base {
	return Base{t: t, r: runID, s: sessionID, k: eventKey, at: occurredAt, p: payload}
}

// Type implements Event.Type.
func (e Base) Type() EventType { return e.t }

// RunID implements Event.RunID.
func (e Base) RunID() string { return e.r }

// SessionID implements Event.SessionID.
func (e Base) SessionID() string { return e.s }

// EventKey implements Event.EventKey.
func (e Base) EventKey() string { return e.k }

// OccurredAt implements Event.OccurredAt.
func (e Base) OccurredAt() time.Time { return e.at }

// Payload implements Event.Payload.
func (e Base) Payload() any { return e.p }

// Text returns the assistant text in the order stored in Messages. It omits
// reasoning, tool calls, and other parts that are not displayed as an answer.
func (p AssistantTurnPayload) Text() string {
	var text strings.Builder
	for _, message := range p.Messages {
		if message.Role != model.ConversationRoleAssistant {
			continue
		}
		text.WriteString(message.Text())
	}
	return text.String()
}
