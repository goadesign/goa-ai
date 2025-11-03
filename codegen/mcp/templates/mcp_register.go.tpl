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

var {{ .Register.HelperName }}ToolSpecs = []tools.ToolSpec{
{{- range .Register.Tools }}
    {
        Name:      {{ printf "%q" .QualifiedName }},
        Service:   {{ printf "%q" $.Register.ServiceName }},
        Toolset:   {{ printf "%q" $.Register.SuiteName }},
        Description: {{ printf "%q" .Description }},
        Payload: tools.TypeSpec{
            Name: {{ printf "%q" .PayloadType }},
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
            Name: {{ printf "%q" .ResultType }},
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

var {{ .Register.HelperName }}ToolSchemas = map[string]string{
{{- range .Register.Tools }}
	{{ printf "%q" .QualifiedName }}: {{ printf "%q" .InputSchema }},
{{- end }}
}

var {{ .Register.HelperName }}ToolExamples = map[string]string{
{{- range .Register.Tools }}
	{{ printf "%q" .QualifiedName }}: {{ printf "%q" .ExampleArgs }},
{{- end }}
}

func Register{{ .Register.HelperName }}(ctx context.Context, rt *agentsruntime.Runtime, caller mcpruntime.Caller) error {
    if rt == nil {
        return errors.New("runtime is required")
    }
    if caller == nil {
        return errors.New("mcp caller is required")
    }
    suite := {{ printf "%q" .Register.SuiteQualifiedName }}
    suitePrefix := suite + "."

    exec := func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
        fullName := call.Name
        toolName := string(fullName)
        if strings.HasPrefix(toolName, suitePrefix) {
            toolName = toolName[len(suitePrefix):]
        }

		payload, err := json.Marshal(call.Payload)
		if err != nil {
			return planner.ToolResult{
				Name: fullName,
			}, err
		}

        resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
            Suite:   suite,
            Tool:    toolName,
            Payload: payload,
        })
        if err != nil {
            return {{ .Register.HelperName }}HandleError(fullName, err), nil
        }

		var value any
		if len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, &value); err != nil {
				return planner.ToolResult{
					Name: fullName,
				}, err
			}
		}

		var toolTelemetry *telemetry.ToolTelemetry
		if len(resp.Structured) > 0 {
			var structured any
			if err := json.Unmarshal(resp.Structured, &structured); err != nil {
				return planner.ToolResult{
					Name: fullName,
				}, err
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
        Name: {{ printf "%q" .Register.SuiteQualifiedName }},
        Description: {{ printf "%q" .Register.Description }},
        Execute: exec,
        Specs: {{ .Register.HelperName }}ToolSpecs,
    })
}

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

func {{ .Register.HelperName }}RetryHint(toolName tools.Ident, err error) *planner.RetryHint {
    key := string(toolName)
    schema := {{ .Register.HelperName }}ToolSchemas[key]
    example := {{ .Register.HelperName }}ToolExamples[key]
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
            prompt := retry.BuildRepairPrompt("tools/call:"+key, rpcErr.Message, example, schema)
            return &planner.RetryHint{
                Reason:         planner.RetryReasonInvalidArguments,
                Tool:           toolName,
                Message:        prompt,
                RestrictToTool: true,
            }
        case mcpruntime.JSONRPCMethodNotFound:
            return &planner.RetryHint{
                Reason: planner.RetryReasonToolUnavailable,
                Tool:   toolName,
                Message: rpcErr.Message,
            }
        }
    }
    return nil
}
