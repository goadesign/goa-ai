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
{{- if .RunPolicy.OnMissingFields }}
            {{- if eq .RunPolicy.OnMissingFields "finalize" }}
            OnMissingFields: agentsruntime.MissingFieldsFinalize,
            {{- else if eq .RunPolicy.OnMissingFields "await_clarification" }}
            OnMissingFields: agentsruntime.MissingFieldsAwaitClarification,
            {{- else if eq .RunPolicy.OnMissingFields "resume" }}
            OnMissingFields: agentsruntime.MissingFieldsResume,
            {{- end }}
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
        // Build a runtime ToolsetRegistration inline to avoid exposing method/service adapters.
        reg := agentsruntime.ToolsetRegistration{
            Name: {{ printf "%q" .QualifiedName }},
            // Provide the agent-wide specs; runtime de-duplicates tool specs across registrations.
            Specs: {{ $.ToolSpecsPackage }}.Specs,
            Execute: func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
                return exec.Execute(ctx, agentsruntime.ToolCallMeta{}, call)
            },
        }
        if err := rt.RegisterToolset(reg); err != nil {
            return err
        }
    }
    {{- end }}
    {{- end }}
    {{- end }}

    // Service toolsets are registered by application code using executors.
    return nil
}

{{- $had := false -}}
{{- range .UsedToolsets }}
{{- if not (and .Expr .Expr.External) }}
{{- $had = true -}}
{{- end }}
{{- end }}
{{- if $had }}
// RegisterUsedToolsets registers all non-external Used toolsets for this agent.
// Provide executors via typed options for each required toolset.
//
// Example:
//   err := RegisterUsedToolsets(ctx, rt,
{{- range .UsedToolsets }}
{{- if not (and .Expr .Expr.External) }}
//       With{{ goify .PathName true }}Executor(exec),
{{- end }}
{{- end }}
//   )
func RegisterUsedToolsets(ctx context.Context, rt *agentsruntime.Runtime, opts ...func(map[string]agentsruntime.ToolCallExecutor)) error {
    if rt == nil {
        return errors.New("runtime is required")
    }
    execs := make(map[string]agentsruntime.ToolCallExecutor)
    for _, o := range opts {
        if o != nil {
            o(execs)
        }
    }
    // Register service (non-external) used toolsets.
    {{- range .UsedToolsets }}
    {{- if not (and .Expr .Expr.External) }}
    {
        const toolsetID = {{ printf "%q" .QualifiedName }}
        exec := execs[toolsetID]
        reg := agentsruntime.ToolsetRegistration{
            Name:  toolsetID,
            Specs: {{ $.ToolSpecsPackage }}.Specs,
            Execute: func(ctx context.Context, call planner.ToolRequest) (planner.ToolResult, error) {
                if exec == nil {
                    return planner.ToolResult{
                        Error: planner.NewToolError("executor is required"),
                    }, nil
                }
                meta := agentsruntime.ToolCallMeta{
                    RunID:            call.RunID,
                    SessionID:        call.SessionID,
                    TurnID:           call.TurnID,
                    ToolCallID:       call.ToolCallID,
                    ParentToolCallID: call.ParentToolCallID,
                }
                return exec.Execute(ctx, meta, call)
            },
        }
        if err := rt.RegisterToolset(reg); err != nil {
            return err
        }
    }
    {{- end }}
    {{- end }}
    return nil
}

{{- range .UsedToolsets }}
{{- if not (and .Expr .Expr.External) }}
// With{{ goify .PathName true }}Executor associates an executor for {{ .QualifiedName }}.
func With{{ goify .PathName true }}Executor(exec agentsruntime.ToolCallExecutor) func(map[string]agentsruntime.ToolCallExecutor) {
    return func(m map[string]agentsruntime.ToolCallExecutor) {
        if exec == nil {
            return
        }
        m[{{ printf "%q" .QualifiedName }}] = exec
    }
}
{{- end }}
{{- end }}
{{- end }}
