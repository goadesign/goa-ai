// Package stream contains streaming abstractions for agent interactions.
package stream

import "context"

// Sink delivers streaming updates (planner thoughts, tool statuses, assistant messages) to clients.
type Sink interface {
	Send(ctx context.Context, event Event) error
	Close(ctx context.Context) error
}

// EventType enumerates stream payload flavors.
type EventType string

const (
	// EventPlannerThought streams planner reasoning snippets.
	EventPlannerThought EventType = "planner_thought"
	// EventToolUpdate streams tool-call status/progress updates.
	EventToolUpdate EventType = "tool_update"
	// EventAssistantReply streams assistant responses incrementally.
	EventAssistantReply EventType = "assistant_reply"
)

// Event is the payload sent across the streaming channel.
type Event struct {
	// Type indicates the kind of streaming event emitted.
	Type EventType
	// RunID ties the event to a workflow run.
	RunID string
	// Content carries the event-specific payload (message chunk, tool status, etc.).
	Content any
}
