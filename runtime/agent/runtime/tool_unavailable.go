package runtime

// tool_unavailable.go defines the runtime-owned "tool unavailable" tool.
//
// This tool is the canonical representation of "the model requested a tool that
// is not available for this run". We keep the transcript/tool handshake
// structurally valid by rewriting unknown or policy-denied tool calls to this
// tool and embedding the originally requested name + payload inside its input.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/template"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	toolUnavailableToolsetName     = "goa-ai.runtime"
	toolUnavailableCallHintPattern = `Tool not available: {{.RequestedTool}}`
)

var (
	toolUnavailableCallHint = template.Must(
		template.New(tools.ToolUnavailable.String()).
			Option("missingkey=error").
			Parse(toolUnavailableCallHintPattern),
	)
	toolUnavailableSpec = tools.ToolSpec{
		Name:        tools.ToolUnavailable,
		Service:     "goa-ai",
		Toolset:     toolUnavailableToolsetName,
		Description: "Runtime-owned tool that represents unavailable tool calls.",
		Payload: tools.TypeSpec{
			Name:        "ToolUnavailablePayload",
			Schema:      mustMarshalToolUnavailableSchema(),
			ExampleJSON: tools.RawJSON(`{"requested_tool":"svc_read_count_events","requested_payload":{"from":"2026-02-06T00:00:00Z"}}`),
			Codec: tools.JSONCodec[any]{
				ToJSON: marshalToolUnavailablePayload,
				FromJSON: func(data []byte) (any, error) {
					return unmarshalToolUnavailablePayload(data)
				},
			},
		},
		Result: tools.TypeSpec{
			Name:   "ToolUnavailableResult",
			Schema: tools.RawJSON(`{"type":"object","additionalProperties":true}`),
			Codec:  tools.AnyJSONCodec,
		},
	}
)

type toolUnavailablePayload struct {
	RequestedTool    string          `json:"requested_tool"`
	RequestedPayload rawjson.Message `json:"requested_payload,omitempty"`
}

func toolUnavailableToolDefinition() *model.ToolDefinition {
	return &model.ToolDefinition{
		Name:        tools.ToolUnavailable.String(),
		Description: "Internal. Used when the model requests a tool that is not available for this run. Always tells the model to choose from the advertised tool list.",
		Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object","properties":{"requested_tool":{"type":"string","minLength":1,"description":"The provider-visible tool name originally requested by the model."},"requested_payload":{"description":"The original JSON payload that the model provided for the unknown tool."}},"required":["requested_tool"],"additionalProperties":false}`)),
	}
}

func toolUnavailableToolsetRegistration() ToolsetRegistration {
	return ToolsetRegistration{
		Name:             toolUnavailableToolsetName,
		Description:      "goa-ai runtime internal tools",
		Inline:           true,
		DecodeInExecutor: true,
		Execute:          executeToolUnavailable,
		Specs:            []tools.ToolSpec{toolUnavailableSpec},
		CallHints: map[tools.Ident]*template.Template{
			tools.ToolUnavailable: toolUnavailableCallHint,
		},
	}
}

func mustMarshalToolUnavailableSchema() tools.RawJSON {
	return toolUnavailableToolDefinition().Input.JSONSchema()
}

func executeToolUnavailable(ctx context.Context, call *planner.ToolRequest) (*ToolExecutionResult, error) {
	decoded, err := unmarshalToolUnavailablePayload(call.Payload)
	if err != nil {
		return Executed(&planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Failure:    buildToolFailureFromPayloadError(err, call.Payload, toolUnavailableSpec),
		}), nil
	}
	requested := decoded.RequestedTool

	toolErr := planner.NewToolError(fmt.Sprintf("tool %q is not available for this run", requested))
	return Executed(&planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInvalidCall,
			Error: toolErr,
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryReplan,
			},
		},
	}), nil
}

// marshalToolUnavailablePayload encodes the runtime-owned payload after its
// concrete type has been established by the tool contract.
func marshalToolUnavailablePayload(value any) ([]byte, error) {
	payload, ok := value.(*toolUnavailablePayload)
	if !ok {
		return nil, fmt.Errorf("tool_unavailable payload has type %T", value)
	}
	return json.Marshal(payload)
}

// unmarshalToolUnavailablePayload enforces the closed runtime-owned schema for
// direct model calls to the internal unavailable-tool marker.
func unmarshalToolUnavailablePayload(data []byte) (*toolUnavailablePayload, error) {
	var wire struct {
		RequestedTool    *string         `json:"requested_tool"`
		RequestedPayload rawjson.Message `json:"requested_payload,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, err
	}
	if wire.RequestedTool == nil {
		return nil, tools.NewValidationError(
			"requested_tool is required",
			[]*tools.FieldIssue{{Field: "requested_tool", Constraint: "missing_field"}},
			nil,
		)
	}
	if *wire.RequestedTool == "" {
		minLen := 1
		return nil, tools.NewValidationError(
			"requested_tool must not be empty",
			[]*tools.FieldIssue{{Field: "requested_tool", Constraint: "invalid_length", MinLen: &minLen}},
			nil,
		)
	}
	return &toolUnavailablePayload{
		RequestedTool:    *wire.RequestedTool,
		RequestedPayload: wire.RequestedPayload,
	}, nil
}

// rewriteToolCallUnavailable rewrites one tool call to the runtime-owned
// tool_unavailable tool while preserving the originally requested tool name and
// payload in the rewritten input.
func (r *Runtime) rewriteToolCallUnavailable(call planner.ToolRequest) (planner.ToolRequest, error) {
	requestedName := call.TranscriptName()
	requestedPayload := call.TranscriptPayload()
	if call.ModelName == "" {
		call.ModelName = requestedName
		call.ModelPayload = append(rawjson.Message(nil), requestedPayload...)
	}
	payload, err := json.Marshal(toolUnavailablePayload{
		RequestedTool:    requestedName.String(),
		RequestedPayload: requestedPayload,
	})
	if err != nil {
		return planner.ToolRequest{}, fmt.Errorf("runtime: encode tool_unavailable payload for %s: %w", call.Name, err)
	}
	call.Name = tools.ToolUnavailable
	call.Payload = rawjson.Message(payload)
	return call, nil
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

		rewritten, err := r.rewriteToolCallUnavailable(call)
		if err != nil {
			return nil, err
		}
		out[i] = rewritten
	}
	return out, nil
}

// rewriteRecoveryCatalogToolCalls converts direct model calls excluded from the
// active recovery catalog into typed unavailable-tool calls. Planner-owned
// await barriers remain subject to strict catalog validation because they
// encode runtime suspension, not a raw model tool request.
func (r *Runtime) rewriteRecoveryCatalogToolCalls(catalog *RecoveryCatalog, result *planner.PlanResult) error {
	if catalog == nil || result == nil || len(result.ToolCalls) == 0 {
		return nil
	}
	allowed := make(map[tools.Ident]struct{}, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		allowed[tool] = struct{}{}
	}
	for i, call := range result.ToolCalls {
		if call.Name == tools.ToolUnavailable {
			continue
		}
		if _, ok := allowed[call.Name]; ok {
			continue
		}
		rewritten, err := r.rewriteToolCallUnavailable(call)
		if err != nil {
			return err
		}
		result.ToolCalls[i] = rewritten
	}
	return nil
}
