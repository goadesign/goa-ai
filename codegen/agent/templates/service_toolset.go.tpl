// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration returns a ToolsetRegistration
// that delegates execution to the provided ToolCallExecutor.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration(exec runtime.ToolCallExecutor) runtime.ToolsetRegistration {
    return runtime.ToolsetRegistration{
        Name:        {{ printf "%q" .Toolset.QualifiedName }},
        Description: {{ printf "%q" .Toolset.Description }},
        Specs:       {{ $.Agent.ToolSpecsPackage }}.Specs,
        Metadata:    policy.ToolMetadata{ID: {{ printf "%q" .Toolset.Name }}, Name: {{ printf "%q" .Toolset.Name }}{{- if .Toolset.Tags }}, Tags: []string{ {{- range $i, $t := .Toolset.Tags }}{{ if $i }}, {{ end }}{{ printf "%q" $t }}{{- end }} }{{- end }}},
        Execute: func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
            if exec == nil {
                return planner.ToolResult{
                    Error: planner.NewToolError("executor is required"),
                }, nil
            }
            meta := runtime.ToolCallMeta{
                RunID:            call.RunID,
                SessionID:        call.SessionID,
                TurnID:           call.TurnID,
                ToolCallID:       call.ToolCallID,
                ParentToolCallID: call.ParentToolCallID,
            }
            return exec.Execute(ctx, meta, call)
        },
    }
}
