{{- define "activityOptionsLiteral" -}}
engine.ActivityOptions{
{{- if ne .Queue "" }}
    Queue: {{ printf "%q" .Queue }},
{{- end }}
{{- if gt .Timeout 0 }}
    Timeout: time.Duration({{ printf "%d" .Timeout }}),
{{- end }}
{{- if or (gt .RetryPolicy.MaxAttempts 0) (gt .RetryPolicy.InitialInterval 0) (ne .RetryPolicy.BackoffCoefficient 0.0) }}
    RetryPolicy: engine.RetryPolicy{
{{- if gt .RetryPolicy.MaxAttempts 0 }}
        MaxAttempts: {{ .RetryPolicy.MaxAttempts }},
{{- end }}
{{- if gt .RetryPolicy.InitialInterval 0 }}
        InitialInterval: time.Duration({{ printf "%d" .RetryPolicy.InitialInterval }}),
{{- end }}
{{- if ne .RetryPolicy.BackoffCoefficient 0.0 }}
        BackoffCoefficient: {{ printf "%g" .RetryPolicy.BackoffCoefficient }},
{{- end }}
    },
{{- end }}
}
{{- end }}

// Register{{ .StructName }} registers the generated agent components with the runtime.
func Register{{ .StructName }}(ctx context.Context, rt *agentsruntime.Runtime, cfg {{ .ConfigType }}) error {
    if rt == nil {
        return errors.New("runtime is required")
    }
    agent, err := New{{ .StructName }}(cfg)
    if err != nil {
        return err
    }
    if err := rt.RegisterAgent(ctx, agentsruntime.AgentRegistration{
        ID:       {{ printf "%q" .ID }},
        Planner:  agent.Planner,
        Workflow: {{ .Runtime.Workflow.DefinitionVar }},
{{- if .Runtime.Activities }}
        Activities: []engine.ActivityDefinition{
{{- range .Runtime.Activities }}
            {{ .DefinitionVar }},
{{- end }}
        },
{{- end }}
{{- if .Runtime.PlanActivity }}
        PlanActivityName: {{ printf "%q" .Runtime.PlanActivity.Name }},
        PlanActivityOptions: {{ template "activityOptionsLiteral" .Runtime.PlanActivity }},
{{- end }}
{{- if .Runtime.ResumeActivity }}
        ResumeActivityName: {{ printf "%q" .Runtime.ResumeActivity.Name }},
        ResumeActivityOptions: {{ template "activityOptionsLiteral" .Runtime.ResumeActivity }},
{{- end }}
{{- if .Runtime.ExecuteTool }}
        ExecuteToolActivity: {{ printf "%q" .Runtime.ExecuteTool.Name }},
{{- end }}
        Specs: {{ .ToolSpecsPackage }}.Specs,
        Policy: agentsruntime.RunPolicy{
{{- if gt .RunPolicy.Caps.MaxToolCalls 0 }}
            MaxToolCalls: {{ .RunPolicy.Caps.MaxToolCalls }},
{{- end }}
{{- if gt .RunPolicy.Caps.MaxConsecutiveFailedToolCalls 0 }}
            MaxConsecutiveFailedToolCalls: {{ .RunPolicy.Caps.MaxConsecutiveFailedToolCalls }},
{{- end }}
{{- if gt .RunPolicy.TimeBudget 0 }}
            TimeBudget: time.Duration({{ printf "%d" .RunPolicy.TimeBudget }}),
{{- end }}
{{- if .RunPolicy.InterruptsAllowed }}
            InterruptsAllowed: true,
{{- end }}
        },
    }); err != nil {
        return err
    }

    // Auto-register service toolsets for method-backed tools.
{{- range .AllToolsets }}
{{- $ts := . }}
{{- $hasMethod := false }}
{{- range .Tools }}{{- if .IsMethodBacked }}{{- $hasMethod = true }}{{- end }}{{- end }}
{{- if $hasMethod }}
    if err := rt.RegisterToolset({{ .PackageName }}.New{{ $.GoName }}{{ .PathName | goify true }}ServiceToolsetRegistration(cfg.{{ .Name | goify true }}Cfg)); err != nil {
        return err
    }
{{- end }}
{{- end }}

{{- if .MCPToolsets }}
    if cfg.MCPCallers == nil {
        return fmt.Errorf("mcp callers are required for agent %s", {{ printf "%q" .ID }})
    }
{{- range .MCPToolsets }}
    if caller := cfg.MCPCallers[{{ .ConstName }}]; caller == nil {
        return fmt.Errorf("mcp caller for %s is required", {{ .ConstName }})
    } else if err := {{ .HelperAlias }}.{{ .HelperFunc }}(ctx, rt, caller); err != nil {
        return fmt.Errorf("register mcp toolset %s: %w", {{ .ConstName }}, err)
    }
{{- end }}
{{- end }}

    return nil
}
