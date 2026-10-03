// {{ .Constructor }} returns a ToolCallExecutor that
// sends tool calls through MCP and uses generated JSON converters for results.
func {{ .Constructor }}(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        switch call.Name {
        {{- range .Tools }}
        case {{ $.SpecsAlias }}.{{ .ConstName }}:
            resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
				Tool:    {{ printf "%q" .LocalName }},
				Payload: json.RawMessage(call.Payload),
				Continuation: call.MCPContinuation,
            })
            if err != nil {
                return runtime.Executed(runtime.MCPCallFailure(call.Name, err)), nil
            }
            if resp.InputRequired != nil {
                return runtime.AwaitMCPInput(resp.InputRequired), nil
            }
            var value any
            {{- if .HasResult }}
            if len(resp.StructuredContent) == 0 {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response is missing structured content")),
                )), nil
            }
            v, err := {{ $.SpecsAlias }}.{{ .SpecVar }}().Result.Codec.FromJSON(resp.StructuredContent)
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
            return runtime.Executed(&planner.ToolResult{
				Name:      call.Name,
				Result:    value,
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
