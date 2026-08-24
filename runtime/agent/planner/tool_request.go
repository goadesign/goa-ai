// Package planner turns generated typed tool descriptors into planner-owned
// call intent. Application input errors are returned to the planner instead of
// terminating the process.
package planner

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

// NewToolRequest encodes payload with the codec paired to tool and returns one
// planner-authored request. The runtime assigns its execution ID after the
// planner returns.
func NewToolRequest[Payload, Result any](
	tool tools.TypedTool[Payload, Result],
	payload Payload,
) (ToolRequest, error) {
	encoded, err := tool.Payload.ToJSON(payload)
	if err != nil {
		return ToolRequest{}, fmt.Errorf("encode %s tool payload: %w", tool.Name, err)
	}
	return ToolRequest{
		Name:    tool.Name,
		Payload: encoded,
	}, nil
}

// ToolRequestFromModelCall forwards one validated model call without asking
// planner code to copy its provider correlation ID.
func ToolRequestFromModelCall(call model.ToolCall) (ToolRequest, error) {
	if call.ID == "" {
		return ToolRequest{}, fmt.Errorf("model tool call %q has no correlation ID", call.Name)
	}
	return ToolRequest{
		Name:            call.Name,
		Payload:         append([]byte(nil), call.Payload...),
		ModelToolCallID: call.ID,
	}, nil
}
