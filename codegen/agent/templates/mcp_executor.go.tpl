// {{ .Constructor }} returns a ToolCallExecutor that
// sends tool calls through MCP and uses generated JSON converters for results.
func {{ .Constructor }}(caller mcpruntime.Caller) runtime.ToolCallExecutor {
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, _ *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
        switch call.Name {
        {{- range .Tools }}
        case {{ $.SpecsAlias }}.{{ .ConstName }}:
            return runtime.ExecuteMCPTool(ctx,caller,call,{{ printf "%q" .LocalName }},{{ $.SpecsAlias }}.{{ .SpecVar }}())
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
