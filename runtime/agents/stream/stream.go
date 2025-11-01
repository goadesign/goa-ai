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
	"time"

	"goa.design/goa-ai/runtime/agents/telemetry"
	"goa.design/goa-ai/runtime/agents/toolerrors"
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

	// Base provides a default implementation of Event. Embed this struct in concrete
	// event types to inherit the Type(), RunID(), and Payload() methods. All stream
	// event types (AssistantReply, ToolStart, etc.) embed Base to avoid boilerplate.
	//
	// Field names are abbreviated to minimize visual clutter when constructing events,
	// since Base fields are rarely accessed directly (consumers use the interface methods
	// or type-assert to concrete types for their specific fields).
	Base struct {
		// T is the event type constant (e.g., EventToolEnd, EventAssistantReply).
		// Set this when constructing concrete events to identify the payload category.
		T EventType
		// R is the workflow run identifier that produced this event. All events from
		// a single run share the same R value, enabling clients to filter or correlate
		// events by run.
		R string
		// P is the JSON-serializable payload returned by the Payload() method. Sinks
		// marshal this value when publishing events. Set P to the appropriate payload
		// type for the event (e.g., ToolStartPayload for ToolStart events).
		P any
	}

	// AssistantReply streams incremental assistant message content as the planner
	// produces the final response. Clients receive these events to display streaming
	// text with a typewriter effect. Multiple AssistantReply events may be sent for
	// a single response as the planner generates content progressively.
	AssistantReply struct {
		Base
		// Text contains the assistant message content (partial or complete chunk).
		// Clients should concatenate Text from sequential AssistantReply events
		// to reconstruct the full message.
		Text string
	}

	// PlannerThought streams planner reasoning and intermediate annotations during
	// execution. These events allow clients to display "thinking..." indicators and
	// show the planner's internal reasoning process before tool calls complete.
	PlannerThought struct {
		Base
		// Note contains the planner's annotation text (e.g., "Analyzing user query...",
		// "Calling weather API to get forecast..."). This is human-readable text
		// suitable for displaying in a UI thought bubble or debug panel.
		Note string
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
		// Payload contains the marshaled tool arguments passed to the activity. This is
		// the input data the tool will process. Nil if no arguments were provided. Clients
		// can display this for debugging or audit logging.
		Payload any `json:"payload,omitempty"`
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

	// ToolEnd streams when a tool activity completes with either a result or error. Clients
	// receive this to update tool status, close progress indicators, display results or errors,
	// and track tool execution metrics. Every ToolStart event eventually produces a ToolEnd.
	ToolEnd struct {
		Base
		// Data contains the structured result metadata for this tool completion. Clients
		// access this field directly for type-safe field access (e.g., event.Data.Duration,
		// event.Data.ToolCallID).
		Data ToolEndPayload
	}

	// ToolEndPayload carries the result metadata for a completed tool invocation. This
	// structure is JSON-serialized when sent over the wire (SSE, WebSocket, Pulse).
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
		// Result contains the tool's output payload. This is the structured data returned
		// by the tool on success. Nil if the tool failed (check Error field). Clients
		// display this as the tool's answer or pass it to downstream processing.
		Result any `json:"result,omitempty"`
		// Duration is the wall-clock execution time for the tool activity, including any
		// queuing delay, retries, and processing time. Clients can display this in
		// performance dashboards or debug panels to identify slow tools.
		Duration time.Duration `json:"duration"`
		// Telemetry holds structured observability metadata collected during tool execution:
		// token counts, model identifiers, retry attempts, and provider-specific metrics.
		// Nil if no telemetry was collected. Clients use this for cost tracking, performance
		// monitoring, and compliance reporting.
		Telemetry *telemetry.ToolTelemetry `json:"telemetry,omitempty"`
		// Error contains any error returned by the tool execution. Nil on success. When
		// non-nil, Result is nil and this field contains structured error details (code,
		// message, retryability). Clients display error messages and may implement retry UIs.
		Error *toolerrors.ToolError `json:"error,omitempty"`
		// Extra carries optional extension data for clients that need to attach
		// transport- or domain-specific fields without breaking the wire contract.
		// The runtime ignores its contents; sinks may include it when present.
		Extra map[string]any `json:"extra,omitempty"`
	}

	// ToolUpdatePayload describes a non-terminal update to a tool call, typically used
	// when a parent tool dynamically discovers more child tools across planning iterations.
	ToolUpdatePayload struct {
		// ToolCallID identifies the (parent) tool call being updated.
		ToolCallID string `json:"tool_call_id"`
		// ExpectedChildrenTotal is the new total of expected child tools.
		ExpectedChildrenTotal int `json:"expected_children_total"`
	}

	// ToolUpdate streams progress updates for a tool call (new expected child count).
	ToolUpdate struct {
		Base
		Data ToolUpdatePayload
	}
)

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

	// EventAssistantReply streams incremental assistant message content as the planner
	// produces the final response. Clients receive text chunks that can be displayed
	// progressively (streaming typewriter effect). Emitted by StreamSubscriber when
	// AssistantMessageEvent hooks fire.
	EventAssistantReply EventType = "assistant_reply"
)

// Type implements Event.Type.
func (e Base) Type() EventType { return e.T }

// RunID implements Event.RunID.
func (e Base) RunID() string { return e.R }

// Payload implements Event.Payload.
func (e Base) Payload() any { return e.P }
