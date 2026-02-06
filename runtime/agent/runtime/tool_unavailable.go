package runtime

// tool_unavailable.go defines the runtime-owned "tool unavailable" tool.
//
// This tool is the canonical representation of "the model requested a tool name
// that is not registered for this run". We keep the transcript/tool handshake
// structurally valid by rewriting unknown tool calls to this tool and embedding
// the originally requested name + payload inside its input.

import (
	"context"
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

const toolUnavailableToolsetName = "goa-ai.runtime"

type toolUnavailablePayload struct {
	RequestedTool    string          `json:"requested_tool"`
	RequestedPayload json.RawMessage `json:"requested_payload,omitempty"`
}

func toolUnavailableToolDefinition() *model.ToolDefinition {
	return &model.ToolDefinition{
		Name:        tools.ToolUnavailable.String(),
		Description: "Internal. Used when the model requests an unknown tool name. Always returns an error with a retry hint to pick a tool from the advertised list.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"requested_tool": map[string]any{
					"type":        "string",
					"description": "The provider-visible tool name originally requested by the model.",
				},
				"requested_payload": map[string]any{
					"description": "The original JSON payload that the model provided for the unknown tool.",
				},
			},
			"required":             []string{"requested_tool"},
			"additionalProperties": false,
		},
	}
}

func toolUnavailableToolsetRegistration() ToolsetRegistration {
	spec := tools.ToolSpec{
		Name:        tools.ToolUnavailable,
		Service:     "goa-ai",
		Toolset:     toolUnavailableToolsetName,
		Description: "Runtime-owned tool that represents unknown tool calls.",
		Payload: tools.TypeSpec{
			Name:        "ToolUnavailablePayload",
			Schema:      mustMarshalToolUnavailableSchema(),
			ExampleJSON: []byte(`{"requested_tool":"atlas_read_count_events","requested_payload":{"from":"2026-02-06T00:00:00Z"}}`),
			Codec:       tools.AnyJSONCodec,
		},
		Result: tools.TypeSpec{
			Name:   "ToolUnavailableResult",
			Schema: []byte(`{"type":"object","additionalProperties":true}`),
			Codec:  tools.AnyJSONCodec,
		},
	}
	return ToolsetRegistration{
		Name:        toolUnavailableToolsetName,
		Description: "goa-ai runtime internal tools",
		Inline:      true,
		Execute:     executeToolUnavailable,
		Specs:       []tools.ToolSpec{spec},
	}
}

func mustMarshalToolUnavailableSchema() []byte {
	schema := toolUnavailableToolDefinition().InputSchema
	data, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Errorf("runtime: marshal tool_unavailable schema: %w", err))
	}
	return data
}

func executeToolUnavailable(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
	requested := ""
	if len(call.Payload) > 0 {
		var p toolUnavailablePayload
		if err := json.Unmarshal(call.Payload, &p); err != nil {
			// This tool is runtime-owned but models can still call it directly.
			// Treat malformed payloads as tool errors so the run can continue.
			toolErr := planner.NewToolError(fmt.Sprintf("tool_unavailable payload is invalid JSON: %v", err))
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Error:      toolErr,
				RetryHint: &planner.RetryHint{
					Reason:         planner.RetryReasonInvalidArguments,
					Tool:           call.Name,
					Message:        "Call tool_unavailable with JSON: {\"requested_tool\": <string>, \"requested_payload\": <json>} (requested_payload is optional).",
					RestrictToTool: true,
				},
			}, nil
		}
		requested = p.RequestedTool
	}
	if requested == "" {
		requested = "<missing requested_tool>"
	}

	toolErr := planner.NewToolError(fmt.Sprintf("unknown tool %q", requested))
	return &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      toolErr,
		RetryHint: &planner.RetryHint{
			Reason:         planner.RetryReasonToolUnavailable,
			Tool:           call.Name,
			RestrictToTool: false,
			Message:        "Tool name is not registered for this run. Choose a tool from the advertised tool list and call it with the exact JSON schema.",
		},
	}, nil
}

func (r *Runtime) rewriteUnknownToolCalls(calls []planner.ToolRequest) ([]planner.ToolRequest, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	out := make([]planner.ToolRequest, len(calls))
	for i, call := range calls {
		if call.Name == "" {
			out[i] = call
			continue
		}
		if _, ok := r.toolSpec(call.Name); ok {
			out[i] = call
			continue
		}

		payload, err := json.Marshal(toolUnavailablePayload{
			RequestedTool:    call.Name.String(),
			RequestedPayload: call.Payload,
		})
		if err != nil {
			return nil, fmt.Errorf("runtime: encode tool_unavailable payload for %s: %w", call.Name, err)
		}
		call.Name = tools.ToolUnavailable
		call.Payload = json.RawMessage(payload)
		out[i] = call
	}
	return out, nil
}
