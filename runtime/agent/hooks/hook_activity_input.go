package hooks

import (
	"encoding/json"

	"goa.design/goa-ai/runtime/agent"
)

// ActivityInput describes a hook event emitted from workflow code and published by the
// hook activity. Payload contains the event-specific fields encoded as JSON.
type ActivityInput struct {
	// Type identifies the hook event variant (for example, ToolCallScheduled).
	Type EventType

	// RunID identifies the run that owns this event.
	RunID string

	// AgentID identifies the agent that owns this event.
	AgentID agent.Ident

	// SessionID identifies the logical session that owns this event.
	SessionID string

	// TurnID groups events for a single conversational turn. Empty when turn tracking is disabled.
	TurnID string

	// Payload holds event-specific fields encoded as JSON.
	Payload json.RawMessage
}
