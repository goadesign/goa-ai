// Package runtime forwards MCP progress from the tool activity to its trusted
// host stream. The runtime supplies invocation identity; transports supply the
// individual request identity and service-reported work values.
package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/mcp"
)

// withMCPProgress attaches a listener for this activity's external requests.
// It emits no durable result or model input; the final execution path owns both.
func (r *Runtime) withMCPProgress(ctx context.Context, call ToolCall) context.Context {
	if call.SessionID == "" || r.streamSubscriber == nil {
		return ctx
	}
	return mcp.WithProgress(ctx, func(ctx context.Context, update mcp.Progress) error {
		payload := stream.ToolProgressPayload{
			ToolCallID:       call.ToolCallID,
			ParentToolCallID: call.ParentToolCallID,
			ToolName:         string(call.Name),
			RequestID:        append(rawjson.Message(nil), update.RequestID...),
			Value:            update.Value,
			Total:            update.Total,
			Message:          update.Message,
		}
		return r.streamSubscriber.HandleToolProgress(ctx, stream.ToolProgress{
			Base: stream.NewBase(stream.EventToolProgress, call.RunID, call.SessionID, payload),
			Data: payload,
		})
	})
}
