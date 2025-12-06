package {{ .Package }}

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	a2aruntime "goa.design/goa-ai/runtime/a2a"
	"goa.design/goa-ai/runtime/a2a/retry"
)

// {{ .HelperName }}ToolSpecs contains the tool specifications for the {{ .AgentName }} agent.
var {{ .HelperName }}ToolSpecs = []tools.ToolSpec{
{{- range .Skills }}
	{
		Name:        {{ printf "%q" .ID }},
		Service:     {{ printf "%q" $.AgentName }},
		Toolset:     {{ printf "%q" $.SuiteQualifiedName }},
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

// Register{{ .HelperName }} registers the {{ .AgentName }} agent's skills with the runtime.
// The caller parameter provides the A2A client for making remote calls.
func Register{{ .HelperName }}(ctx context.Context, rt *agentsruntime.Runtime, caller a2aruntime.Caller) error {
	if rt == nil {
		return errors.New("runtime is required")
	}
	if caller == nil {
		return errors.New("a2a caller is required")
	}

	exec := func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
		fullName := call.Name
		skillName := string(fullName)
		const suitePrefix = {{ printf "%q" .SuiteQualifiedName }} + "."
		if strings.HasPrefix(skillName, suitePrefix) {
			skillName = skillName[len(suitePrefix):]
		}

		payload, err := json.Marshal(call.Payload)
		if err != nil {
			return planner.ToolResult{Name: fullName}, err
		}

		resp, err := caller.SendTask(ctx, a2aruntime.SendTaskRequest{
			Suite:   {{ printf "%q" .SuiteQualifiedName }},
			Skill:   skillName,
			Payload: payload,
		})
		if err != nil {
			return {{ .HelperName }}HandleError(fullName, err), nil
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
		Name:        {{ printf "%q" .SuiteQualifiedName }},
		Description: {{ printf "%q" .Description }},
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
		Specs:            {{ .HelperName }}ToolSpecs,
		DecodeInExecutor: true,
	})
}

// {{ .HelperName }}HandleError converts an error into a tool result with appropriate retry hints.
func {{ .HelperName }}HandleError(toolName tools.Ident, err error) planner.ToolResult {
	result := planner.ToolResult{
		Name:  toolName,
		Error: planner.ToolErrorFromError(err),
	}
	if hint := {{ .HelperName }}RetryHint(toolName, err); hint != nil {
		result.RetryHint = hint
	}
	return result
}

// {{ .HelperName }}RetryHint determines if an error should trigger a retry and returns appropriate hints.
func {{ .HelperName }}RetryHint(toolName tools.Ident, err error) *planner.RetryHint {
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
	var rpcErr *a2aruntime.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case a2aruntime.JSONRPCInvalidParams:
			// Schema and example are known at generation time - use switch for direct lookup
			var schemaJSON, example string
			switch key {
	{{- range .Skills }}
			case {{ printf "%q" .ID }}:
				schemaJSON = {{ printf "%q" .InputSchema }}
				example = {{ printf "%q" .ExampleArgs }}
	{{- end }}
			}
			prompt := retry.BuildRepairPrompt("tasks/send:"+key, rpcErr.Message, example, schemaJSON)
			return &planner.RetryHint{
				Reason:         planner.RetryReasonInvalidArguments,
				Tool:           toolName,
				Message:        prompt,
				RestrictToTool: true,
			}
		case a2aruntime.JSONRPCMethodNotFound:
			return &planner.RetryHint{
				Reason:  planner.RetryReasonToolUnavailable,
				Tool:    toolName,
				Message: rpcErr.Message,
			}
		}
	}
	return nil
}
