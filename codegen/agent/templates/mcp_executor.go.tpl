// {{ .Constructor }} returns a ToolCallExecutor that
// sends tool calls through MCP and uses generated JSON converters for results.
func {{ .Constructor }}(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        if call == nil {
            return runtime.Executed({{ .Failure }}("", planner.FailureInternal, planner.RecoveryFinish, errors.New("tool request is nil"))), nil
        }
        if meta == nil {
            return runtime.Executed({{ .Failure }}(call.Name, planner.FailureInternal, planner.RecoveryFinish, errors.New("tool call meta is nil"))), nil
        }
        switch call.Name {
        {{- range .Tools }}
        case {{ $.SpecsAlias }}.{{ .ConstName }}:
            resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
				Tool:    {{ printf "%q" .LocalName }},
				Payload: json.RawMessage(call.Payload),
            })
            if err != nil {
                return runtime.Executed(runtime.MCPCallFailure(call.Name, err)), nil
            }
            var value any
            {{- if .StructuredResult }}
            if len(resp.StructuredContent) == 0 {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response is missing structured content")),
                )), nil
            }
            v, err := {{ $.SpecsAlias }}.{{ .SpecVar }}().Result.Codec.FromJSON(resp.StructuredContent)
            {{- else if .TextResult }}
            if len(resp.Content) != 1 {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response must contain one text result")),
                )), nil
            }
            text, ok := resp.Content[0].(*mcpruntime.TextContent)
            if !ok {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response result must be text")),
                )), nil
            }
            encoded, err := json.Marshal(text.Text)
            if err != nil {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureInternal,
                    planner.RecoveryFinish,
                    err,
                )), nil
            }
            v, err := {{ $.SpecsAlias }}.{{ .SpecVar }}().Result.Codec.FromJSON(encoded)
            {{- else if .HasResult }}
            if len(resp.Content) != 1 {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response must contain one text result")),
                )), nil
            }
            text, ok := resp.Content[0].(*mcpruntime.TextContent)
            if !ok {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response result must be text")),
                )), nil
            }
            v, err := {{ $.SpecsAlias }}.{{ .SpecVar }}().Result.Codec.FromJSON([]byte(text.Text))
            {{- else }}
            if len(resp.Content) != 0 || len(resp.StructuredContent) != 0 {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response for a method without a result must be empty")),
                )), nil
            }
            {{- end }}
            {{- if .HasResult }}
            if err != nil {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    err,
                )), nil
            }
            value = v
            {{- end }}
            var tel *telemetry.ToolTelemetry
            if len(resp.StructuredContent) > 0 {
                tel = &telemetry.ToolTelemetry{
					Extra: map[string]any{
						"structured": json.RawMessage(resp.StructuredContent),
					},
				}
            }
            return runtime.Executed(&planner.ToolResult{
				Name:      call.Name,
				Result:    value,
				Telemetry: tel,
			}), nil
        {{- end }}
        default:
            return runtime.Executed({{ .Failure }}(
                call.Name,
                planner.FailureInvalidCall,
                planner.RecoveryReplan,
                errors.New("unknown MCP tool"),
            )), nil
        }
    })
}

// {{ .Failure }} constructs a classified MCP tool failure.
func {{ .Failure }}(name tools.Ident, kind planner.FailureKind, action planner.RecoveryAction, err error) *planner.ToolResult {
    return &planner.ToolResult{
        Name: name,
        Failure: &planner.ToolFailure{
            Kind:  kind,
            Error: planner.ToolErrorFromError(err),
            Recovery: planner.RecoveryDirective{
                Action: action,
            },
        },
    }
}
