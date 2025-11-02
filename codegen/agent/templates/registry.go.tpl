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
        ID:      {{ printf "%q" .ID }},
        Planner: agent.Planner,
        Workflow: engine.WorkflowDefinition{
            Name:      {{ printf "%q" .Runtime.Workflow.Name }},
            TaskQueue: {{ printf "%q" .Runtime.Workflow.Queue }},
            Handler:   agentsruntime.WorkflowHandler(rt),
        },
{{- if .Runtime.Activities }}
        Activities: []engine.ActivityDefinition{
{{- range .Runtime.Activities }}
            {
                Name: {{ printf "%q" .Name }},
{{- if eq .Kind "plan" }}
                Handler: agentsruntime.PlanStartActivityHandler(rt),
{{- else if eq .Kind "resume" }}
                Handler: agentsruntime.PlanResumeActivityHandler(rt),
{{- else if eq .Kind "execute_tool" }}
                Handler: agentsruntime.ExecuteToolActivityHandler(rt),
{{- else }}
                Handler: func(context.Context, any) (any, error) { return nil, errors.New("activity not implemented") },
{{- end }}
                Options: {{ template "activityOptionsLiteral" . }},
            },
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
        {{- if .Tools }}
        Specs: {{ .ToolSpecsPackage }}.Specs,
        {{- else }}
        Specs: nil,
        {{- end }}
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

    {{- if .HasExternalMCP }}
    // Register external MCP toolsets using local executors and callers from config.
    if cfg.MCPCallers == nil {
        return fmt.Errorf("mcp callers are required for agent %s", {{ printf "%q" .ID }})
    }
    {{- range .AllToolsets }}
    {{- if and .Expr (eq .Expr.External true) }}
    {
        caller := cfg.MCPCallers[{{ .MCP.ConstName }}]
        if caller == nil {
            return fmt.Errorf("mcp caller for %s is required", {{ .MCP.ConstName }})
        }
        exec := {{ .PackageName }}.New{{ $.GoName }}{{ goify .PathName true }}MCPExecutor(caller)
        if err := rt.RegisterToolset({{ .PackageName }}.New{{ $.GoName }}{{ goify .PathName true }}ToolsetRegistration(exec)); err != nil {
            return err
        }
    }
    {{- end }}
    {{- end }}
    {{- end }}

    // Service toolsets are registered by application code using executors.
    return nil
}
