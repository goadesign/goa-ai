package {{ .Register.Package }}

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	"goa.design/goa-ai/runtime/mcp/retry"
)

// {{ .Register.HelperName }}ToolSpecs contains the tool specifications for the {{ .Register.SuiteName }} toolset.
var {{ .Register.HelperName }}ToolSpecs = []tools.ToolSpec{
{{- range .Register.Tools }}
	{
		Name:        {{ printf "%q" .ID }},
		Service:     {{ printf "%q" $.Register.ServiceName }},
		Toolset:     {{ printf "%q" $.Register.SuiteQualifiedName }},
		Description: {{ printf "%q" .Description }},
		Payload: tools.TypeSpec{
			Name:   {{ printf "%q" .PayloadType }},
			Schema: []byte({{ printf "%q" .InputSchema }}),
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
				FromJSON: func(data []byte) (any, error) {
					if len(data) == 0 {
						return nil, nil
					}
					var out any
					if err := json.Unmarshal(data, &out); err != nil {
						return nil, err
					}
					return out, nil
				},
			},
		},
		Result: tools.TypeSpec{
			Name:   {{ printf "%q" .ResultType }},
			Schema: nil,
			Codec: tools.JSONCodec[any]{
				ToJSON: func(v any) ([]byte, error) {
					return json.Marshal(v)
				},
				FromJSON: func(data []byte) (any, error) {
					if len(data) == 0 {
						return nil, nil
					}
					var out any
					if err := json.Unmarshal(data, &out); err != nil {
						return nil, err
					}
					return out, nil
				},
			},
		},
	},
{{- end }}
}

// Register{{ .Register.HelperName }} registers the {{ .Register.SuiteName }} toolset with the runtime.
// The caller parameter provides the MCP client for making remote calls.
func Register{{ .Register.HelperName }}(ctx context.Context, rt *agentsruntime.Runtime, caller mcpruntime.Caller) error {
	if rt == nil {
		return errors.New("runtime is required")
	}
	if caller == nil {
		return errors.New("mcp caller is required")
	}

	exec := func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
		fullName := call.Name
		toolName := string(fullName)
		const suitePrefix = {{ printf "%q" .Register.SuiteQualifiedName }} + "."
		if strings.HasPrefix(toolName, suitePrefix) {
			toolName = toolName[len(suitePrefix):]
		}

		payload, err := json.Marshal(call.Payload)
		if err != nil {
			return planner.ToolResult{Name: fullName}, err
		}

		resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
			Suite:   {{ printf "%q" .Register.SuiteQualifiedName }},
			Tool:    toolName,
			Payload: payload,
		})
		if err != nil {
			return {{ .Register.HelperName }}HandleError(fullName, err), nil
		}

		var value any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &value); err != nil {
				return planner.ToolResult{Name: fullName}, err
			}
		}

		var toolTelemetry *telemetry.ToolTelemetry
		if len(resp.Structured) > 0 {
			var structured any
			if err := json.Unmarshal(resp.Structured, &structured); err != nil {
				return planner.ToolResult{Name: fullName}, err
			}
			toolTelemetry = &telemetry.ToolTelemetry{
				Extra: map[string]any{"structured": structured},
			}
		}

		return planner.ToolResult{
			Name:      fullName,
			Result:    value,
			Telemetry: toolTelemetry,
		}, nil
	}

	return rt.RegisterToolset(agentsruntime.ToolsetRegistration{
		Name:        {{ printf "%q" .Register.SuiteQualifiedName }},
		Description: {{ printf "%q" .Register.Description }},
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if call == nil {
				return nil, errors.New("tool request is nil")
			}
			out, err := exec(ctx, *call)
			if err != nil {
				return nil, err
			}
			return &out, nil
		},
		Specs:            {{ .Register.HelperName }}ToolSpecs,
		DecodeInExecutor: true,
	})
}

// {{ .Register.HelperName }}HandleError converts an error into a tool result with appropriate retry hints.
func {{ .Register.HelperName }}HandleError(toolName tools.Ident, err error) planner.ToolResult {
	result := planner.ToolResult{
		Name:  toolName,
		Error: planner.ToolErrorFromError(err),
	}
	if hint := {{ .Register.HelperName }}RetryHint(toolName, err); hint != nil {
		result.RetryHint = hint
	}
	return result
}

// {{ .Register.HelperName }}RetryHint determines if an error should trigger a retry and returns appropriate hints.
func {{ .Register.HelperName }}RetryHint(toolName tools.Ident, err error) *planner.RetryHint {
	key := string(toolName)
	var retryErr *retry.RetryableError
	if errors.As(err, &retryErr) {
		return &planner.RetryHint{
			Reason:         planner.RetryReasonInvalidArguments,
			Tool:           toolName,
			Message:        retryErr.Prompt,
			RestrictToTool: true,
		}
	}
	var rpcErr *mcpruntime.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case mcpruntime.JSONRPCInvalidParams:
			// Schema and example are known at generation time - use switch for direct lookup
			var schemaJSON, example string
			switch key {
	{{- range .Register.Tools }}
			case {{ printf "%q" .ID }}:
				schemaJSON = {{ printf "%q" .InputSchema }}
				example = {{ printf "%q" .ExampleArgs }}
	{{- end }}
			}
			prompt := retry.BuildRepairPrompt("tools/call:"+key, rpcErr.Message, example, schemaJSON)
			return &planner.RetryHint{
				Reason:         planner.RetryReasonInvalidArguments,
				Tool:           toolName,
				Message:        prompt,
				RestrictToTool: true,
			}
		case mcpruntime.JSONRPCMethodNotFound:
			return &planner.RetryHint{
				Reason:  planner.RetryReasonToolUnavailable,
				Tool:    toolName,
				Message: rpcErr.Message,
			}
		}
	}
	return nil
}
