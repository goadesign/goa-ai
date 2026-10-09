// Package stream delivers non-terminal tool work updates to the trusted host.
// Request IDs distinguish concurrent MCP requests and retries; these updates
// never become tool results, model input, or durable continuation state.
package stream

import (
	"context"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// ToolProgress carries one work update while its tool call is running.
	ToolProgress struct {
		Base
		// Data contains request and invocation identity with the reported work.
		Data ToolProgressPayload
	}

	// ToolProgressPayload preserves a service's values for one transport request.
	// A new request, including a retry, starts a separate progress sequence.
	ToolProgressPayload struct {
		// ToolCallID identifies the runtime-owned invocation.
		ToolCallID string `json:"tool_call_id"`
		// ParentToolCallID identifies the enclosing invocation when present.
		ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
		// ToolName is the registered tool identifier.
		ToolName string `json:"tool_name"`
		// RequestID is the exact JSON-RPC request ID encoded as JSON.
		RequestID rawjson.Message `json:"request_id"`
		// Value is the service's increasing amount of completed work.
		Value float64 `json:"value"`
		// Total is present when the service knows the total work.
		Total *float64 `json:"total,omitempty"`
		// Message is the service's explanation of its current work.
		Message *string `json:"message,omitempty"`
	}
)

// EventToolProgress identifies work reported before a tool's terminal result.
const EventToolProgress EventType = "tool_progress"

// HandleToolProgress applies the host's profile and delivers one update. The
// caller has already validated its request identity and increasing values.
func (s *Subscriber) HandleToolProgress(ctx context.Context, event ToolProgress) error {
	if !s.profile.ToolProgress {
		return nil
	}
	return s.sink.Send(ctx, event)
}
