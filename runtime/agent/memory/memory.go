// Package memory exposes agent memory storage contracts and helpers for
// persisting and retrieving agent run history. Memory stores record the
// chronological sequence of messages, tool calls, and results so planners
// can reference prior turns when generating responses.
package memory

import (
	"context"
	"time"
)

type (
	// Store persists agent run history so planners and tooling can inspect prior
	// turns. Implementations must be thread-safe and handle concurrent reads/writes
	// to the same run. Production deployments typically use a durable backend
	// (MongoDB, DynamoDB, etc.); see features/memory/mongo for an example.
	Store interface {
		// LoadRun retrieves the snapshot for the given agent and run. Returns an empty
		// snapshot (not an error) if the run doesn't exist yet, allowing callers to
		// treat absence as empty history. Returns an error only for storage failures
		// or connectivity issues.
		LoadRun(ctx context.Context, agentID, runID string) (Snapshot, error)

		// AppendEvents appends events to the run's history. Events should be written
		// atomically if the backend supports it. Returns an error if the write fails.
		// Implementations may deduplicate or reject events based on timestamps or IDs.
		AppendEvents(ctx context.Context, agentID, runID string, events ...Event) error
	}

	// Snapshot captures the durable state of a run at a point in time. Snapshots are
	// immutable once returned by LoadRun; concurrent writes create new snapshots.
	Snapshot struct {
		// AgentID identifies the agent that produced this run.
		AgentID string
		// RunID identifies the workflow run associated with this snapshot.
		RunID string
		// Events lists the chronological memory events persisted so far, ordered by
		// Timestamp ascending. Empty if the run has no history yet.
		Events []Event
		// Meta carries implementation-defined metadata such as database cursors,
		// version numbers, or sync tokens. Planners should not rely on these fields.
		Meta map[string]any
	}

	// Event describes a single entry persisted to the memory store. Events form a
	// chronological log of the agent's interactions, tool invocations, and responses.
	Event struct {
		// Type indicates the category of the event (message, tool call, result, etc.).
		Type EventType
		// Timestamp marks when the event occurred, used for ordering and filtering.
		Timestamp time.Time
		// Data holds the event-specific payload (message content, tool args, results, etc.).
		// The structure depends on Type: user/assistant messages contain strings or
		// structured data, tool calls contain arguments, tool results contain return values.
		Data any
		// Labels provides structured metadata for filtering, querying, or policy decisions.
		// Examples: {"role": "user"}, {"tool": "search"}, {"error": "timeout"}.
		Labels map[string]string
	}

	// Reader provides read-only access to a snapshot, used by planners to query
	// prior turns. Implementations typically wrap a Snapshot and provide
	// convenience methods for filtering and lookup.
	Reader interface {
		// Events returns all events in chronological order.
		Events() []Event

		// FilterByType returns events matching the given type, preserving chronological order.
		FilterByType(t EventType) []Event

		// Latest returns the most recent event of the given type. The boolean return
		// indicates whether an event was found (false if no events of that type exist).
		Latest(t EventType) (Event, bool)
	}

	// Annotation represents planner-supplied metadata appended during execution.
	// Annotations are typically persisted as EventAnnotation entries.
	Annotation struct {
		// Message is the textual annotation provided by the planner or policy engine.
		Message string
		// Labels carries structured metadata associated with the annotation, used for
		// filtering or categorization (e.g., {"severity": "warning"}).
		Labels map[string]string
	}
)

// EventType enumerates persisted memory event categories. Each type corresponds
// to a different kind of interaction or system event during agent execution.
type EventType string

const (
	// EventUserMessage records an end-user utterance or input message.
	EventUserMessage EventType = "user_message"

	// EventAssistantMessage records an assistant response or output message.
	EventAssistantMessage EventType = "assistant_message"

	// EventToolCall records a tool invocation request, including the tool name
	// and arguments passed to it.
	EventToolCall EventType = "tool_call"

	// EventToolResult records the outcome of a tool invocation, including the
	// return value or error.
	EventToolResult EventType = "tool_result"

	// EventPlannerNote records planner-generated notes, thoughts, or reasoning
	// steps emitted during plan generation.
	EventPlannerNote EventType = "planner_note"

	// EventAnnotation records arbitrary annotations injected by policy engines,
	// hooks, or external systems for observability or debugging.
	EventAnnotation EventType = "annotation"
)
