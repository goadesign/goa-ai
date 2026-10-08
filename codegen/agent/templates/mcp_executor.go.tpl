// {{ .Constructor }} returns a ToolCallExecutor that
// sends tool calls through MCP and uses generated JSON converters for results.
func {{ .Constructor }}(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        if call.TextOnly {
            ctx = mcpruntime.WithoutHostInput(ctx)
        }
        if err := toolregistry.ValidateExecution(call.ExecutionSequence, call.ExecutionContinuation, call.TextOnly); err != nil {
            return runtime.Executed({{ .Failure }}(call.Name, planner.FailureInvalidCall, planner.RecoveryFinish, err)), nil
        }
        var inputContinuation *mcpruntime.CallContinuation
        if call.ExecutionContinuation != nil {
            var ok bool
            inputContinuation, ok = call.ExecutionContinuation.AsInput()
            if !ok {
                return runtime.Executed({{ .Failure }}(call.Name, planner.FailureInvalidCall, planner.RecoveryFinish, errors.New("MCP Task execution is not enabled"))), nil
            }
        }
        switch call.Name {
        {{- range .Tools }}
        case {{ $.SpecsAlias }}.{{ .ConstName }}:
            resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
				Tool:    {{ printf "%q" .LocalName }},
				Payload: json.RawMessage(call.Payload),
				Continuation: inputContinuation,
            })
            if err != nil {
                return runtime.Executed(runtime.MCPCallFailure(call.Name, err)), nil
            }
            if resp.InputRequired != nil {
                return runtime.AwaitMCPInput(resp.InputRequired), nil
            }
            if err := resp.Content.Validate(); err != nil {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(err),
                )), nil
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
            if len(resp.StructuredContent) != 0 {
                return runtime.Executed({{ $.Failure }}(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    mcpruntime.NewMalformedResponseError(errors.New("MCP response for a method without a result must omit structured content")),
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
				Blocks:    resp.Content.Clone(),
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
