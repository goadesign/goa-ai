// ToolsetID is the qualified identifier for this toolset.
const ToolsetID = {{ printf "%q" .Toolset.QualifiedName }}

// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration returns a ToolsetRegistration
// that delegates execution to the provided ToolCallExecutor.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration(exec runtime.ToolCallExecutor) runtime.ToolsetRegistration {
    ts := runtime.ToolsetRegistration{
        Name:        ToolsetID,
        Description: {{ printf "%q" .Toolset.Description }},
        Specs:       {{ $.Agent.ToolSpecsPackage }}.Specs,
        Metadata:    policy.ToolMetadata{Title: {{ printf "%q" .Toolset.Title }}{{- if .Toolset.Tags }}, Tags: []string{ {{- range $i, $t := .Toolset.Tags }}{{ if $i }}, {{ end }}{{ printf "%q" $t }}{{- end }} }{{- end }}},
        Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
            if call == nil {
                return nil, fmt.Errorf("tool request is nil")
            }
            if exec == nil {
                return &planner.ToolResult{
                    Error: planner.NewToolError(
                        fmt.Sprintf(
                            "no executor configured for toolset %q; pass a non-nil ToolCallExecutor to New%v%vToolsetRegistration",
                            ToolsetID,
                            "{{ .Agent.GoName }}",
                            "{{ goify .Toolset.PathName true }}",
                        ),
                    ),
                }, nil
            }
            meta := &runtime.ToolCallMeta{
                RunID:            call.RunID,
                SessionID:        call.SessionID,
                TurnID:           call.TurnID,
                ToolCallID:       call.ToolCallID,
                ParentToolCallID: call.ParentToolCallID,
            }
            result, err := exec.Execute(ctx, meta, call)
            if err != nil {
                return nil, err
            }
            if result == nil {
                return nil, fmt.Errorf("executor returned nil result")
            }
            return result, nil
        },
    }
    // Install DSL-provided hint templates when present.
    {
        // Build maps only when at least one template exists to avoid overhead.
        var callRaw map[tools.Ident]string
        var resultRaw map[tools.Ident]string
        {{- range .Toolset.Tools }}
        {{- if .CallHintTemplate }}
        if callRaw == nil { callRaw = make(map[tools.Ident]string) }
        callRaw[tools.Ident({{ printf "%q" .Name }})] = {{ printf "%q" .CallHintTemplate }}
        {{- end }}
        {{- if .ResultHintTemplate }}
        if resultRaw == nil { resultRaw = make(map[tools.Ident]string) }
        resultRaw[tools.Ident({{ printf "%q" .Name }})] = {{ printf "%q" .ResultHintTemplate }}
        {{- end }}
        {{- end }}
        if len(callRaw) > 0 {
            if compiled, err := hints.CompileHintTemplates(callRaw, nil); err == nil { ts.CallHints = compiled }
        }
        if len(resultRaw) > 0 {
            if compiled, err := hints.CompileHintTemplates(resultRaw, nil); err == nil { ts.ResultHints = compiled }
        }
    }
    return ts
}


