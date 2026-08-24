// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}MCPExecutor returns a ToolCallExecutor that
// proxies tool calls to an MCP caller using generated per-toolset codecs.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}MCPExecutor(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    suite := {{ printf "%q" .Toolset.QualifiedName }}

    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        if call == nil {
            return runtime.Executed(failedMCPToolResult("", planner.FailureInternal, planner.RecoveryFinish, errors.New("tool request is nil"))), nil
        }
        if meta == nil {
            return runtime.Executed(failedMCPToolResult(call.Name, planner.FailureInternal, planner.RecoveryFinish, errors.New("tool call meta is nil"))), nil
        }
        switch call.Name {
        {{- range .Tools }}
        case {{ $.Toolset.SpecsPackageName }}.{{ .ConstName }}:
            resp, err := caller.CallTool(ctx, mcpruntime.CallRequest{
				Suite:   suite,
				Tool:    {{ printf "%q" .LocalName }},
				Payload: json.RawMessage(call.Payload),
            })
            if err != nil {
                return runtime.Executed(mcpCallFailure(call, err)), nil
            }
            var value any
            {{- if .HasResult }}
            v, err := {{ $.Toolset.SpecsPackageName }}.Spec{{ .ConstName }}().Result.Codec.FromJSON(resp.Result)
            if err != nil {
                return runtime.Executed(failedMCPToolResult(
                    call.Name,
                    planner.FailureMalformedResult,
                    planner.RecoveryFinish,
                    err,
                )), nil
            }
            value = v
            {{- end }}
            var tel *telemetry.ToolTelemetry
            if len(resp.Structured) > 0 {
                tel = &telemetry.ToolTelemetry{
					Extra: map[string]any{
						"structured": json.RawMessage(resp.Structured),
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
            return runtime.Executed(failedMCPToolResult(
                call.Name,
                planner.FailureInvalidCall,
                planner.RecoveryReplan,
                errors.New("unknown MCP tool"),
            )), nil
        }
    })
}

// mcpCallFailure classifies MCP protocol and transport failures without
// converting error text into control flow.
func mcpCallFailure(call *runtime.ToolCall, err error) *planner.ToolResult {
    kind := planner.FailureUnavailable
    action := planner.RecoveryReplan
    if errors.Is(err, context.DeadlineExceeded) {
        return failedMCPToolResult(call.Name, planner.FailureTimeout, planner.RecoveryFinish, err)
    }
    var malformed *mcpruntime.MalformedResponseError
    if errors.As(err, &malformed) {
        return failedMCPToolResult(call.Name, planner.FailureMalformedResult, planner.RecoveryFinish, err)
    }
    var internal *mcpruntime.InternalError
    if errors.As(err, &internal) {
        return failedMCPToolResult(call.Name, planner.FailureInternal, planner.RecoveryFinish, err)
    }
    var execution *mcpruntime.ToolExecutionError
    if errors.As(err, &execution) {
        return failedMCPToolResult(call.Name, planner.FailureDomainRejection, planner.RecoveryReplan, err)
    }
    var rpcErr *mcpruntime.Error
    if errors.As(err, &rpcErr) {
        switch rpcErr.Code {
        case mcpruntime.JSONRPCInvalidParams:
            return &planner.ToolResult{
                Name: call.Name,
                Failure: &planner.ToolFailure{
                    Kind:  planner.FailureInvalidCall,
                    Error: planner.ToolErrorFromError(err),
                    Recovery: planner.RecoveryDirective{
                        Action: planner.RecoveryCorrectCall,
                    },
                },
            }
        case mcpruntime.JSONRPCMethodNotFound:
            kind = planner.FailureInvalidCall
        default:
            kind = planner.FailureInternal
            action = planner.RecoveryFinish
        }
    }
    return failedMCPToolResult(call.Name, kind, action, err)
}

// failedMCPToolResult constructs a classified MCP tool failure.
func failedMCPToolResult(name tools.Ident, kind planner.FailureKind, action planner.RecoveryAction, err error) *planner.ToolResult {
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
