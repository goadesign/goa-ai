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
            Handler:   rt.ExecuteWorkflow,
        },
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
        ExecuteToolActivityOptions: {{ template "activityOptionsLiteral" .Runtime.ExecuteTool }},
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
{{- if .RunPolicy.History }}
            History: func() agentsruntime.HistoryPolicy {
            {{- if eq .RunPolicy.History.Mode "keep_recent" }}
                return agentsruntime.KeepRecentTurns({{ .RunPolicy.History.KeepRecent }})
            {{- else if eq .RunPolicy.History.Mode "compress" }}
                return agentsruntime.Compress({{ .RunPolicy.History.TriggerAt }}, {{ .RunPolicy.History.CompressKeepRecent }}, cfg.HistoryModel)
            {{- end }}
            }(),
{{- end }}
{{- if or .RunPolicy.Cache.AfterSystem .RunPolicy.Cache.AfterTools }}
            Cache: agentsruntime.CachePolicy{
            {{- if .RunPolicy.Cache.AfterSystem }}
                AfterSystem: true,
            {{- end }}
            {{- if .RunPolicy.Cache.AfterTools }}
                AfterTools: true,
            {{- end }}
            },
{{- end }}
        },
    }); err != nil {
        return err
    }

    {{- if .HasExternalMCP }}
    // Register MCP-backed toolsets using local executors and callers from config.
    if cfg.MCPCallers == nil {
        return fmt.Errorf("mcp callers are required for agent %s", {{ printf "%q" .ID }})
    }
    {{- range .AllToolsets }}
    {{- if isMCPBacked . }}
    {
        caller := cfg.MCPCallers[{{ .MCP.ConstName }}]
        if caller == nil {
            return fmt.Errorf("mcp caller for %s is required", {{ .MCP.ConstName }})
        }
        exec := {{ .PackageName }}.New{{ $.GoName }}{{ goify .PathName true }}MCPExecutor(caller)
        // Build a runtime ToolsetRegistration inline to avoid exposing method/service adapters.
        reg := agentsruntime.ToolsetRegistration{
            Name: {{ printf "%q" .QualifiedName }},
            // Use the used-toolset specs package for strong-contract payload/result codecs.
            Specs: {{ .SpecsPackageName }}.Specs,
            Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
                if call == nil {
                    return nil, fmt.Errorf("tool request is nil")
                }
                meta := &agentsruntime.ToolCallMeta{
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
        if err := rt.RegisterToolset(reg); err != nil {
            return err
        }
    }
    {{- end }}
    {{- end }}
    {{- end }}

    // Service-backed toolsets (method-backed Used toolsets) are registered by
    // application code using executors. Agent-exported toolsets are wired via
    // provider agenttools helpers and consumer-side agent toolset helpers.
    return nil
}

{{- $had := false -}}
{{- range .UsedToolsets }}
{{- if and (not (isMCPBacked .)) (eq .AgentToolsImportPath "") }}
{{- $had = true -}}
{{- end }}
{{- end }}
{{- if $had }}
// RegisterUsedToolsets registers all non-MCP Used toolsets for this agent.
// Provide executors via typed options for each required toolset.
//
// Example:
//   err := RegisterUsedToolsets(ctx, rt,
{{- range .UsedToolsets }}
{{- if and (not (isMCPBacked .)) (eq .AgentToolsImportPath "") }}
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
    // Register non-MCP used toolsets that are not provided by agent-as-tool exports.
    {{- range .UsedToolsets }}
    {{- if and (not (isMCPBacked .)) (eq .AgentToolsImportPath "") }}
    {
        const toolsetID = {{ printf "%q" .QualifiedName }}
        exec := execs[toolsetID]
        reg := agentsruntime.ToolsetRegistration{
            Name:  toolsetID,
            Specs: {{ .SpecsPackageName }}.Specs,
            Execute: func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
                if call == nil {
                    return nil, fmt.Errorf("tool request is nil")
                }
                if exec == nil {
                    return &planner.ToolResult{
                        Error: planner.NewToolError(
                            fmt.Sprintf(
                                "no executor registered for toolset %q; ensure the appropriate With...Executor is wired in RegisterUsedToolsets",
                                toolsetID,
                            ),
                        ),
                    }, nil
                }
                meta := &agentsruntime.ToolCallMeta{
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
            {{- range .Tools }}
            {{- if .CallHintTemplate }}
            if callRaw == nil {
                callRaw = make(map[tools.Ident]string)
            }
            // Use the canonical tool identifier so hints align with Specs and runtime events.
            callRaw[tools.Ident({{ printf "%q" .QualifiedName }})] = {{ printf "%q" .CallHintTemplate }}
            {{- end }}
            {{- if .ResultHintTemplate }}
            if resultRaw == nil {
                resultRaw = make(map[tools.Ident]string)
            }
            // Use the canonical tool identifier so hints align with Specs and runtime events.
            resultRaw[tools.Ident({{ printf "%q" .QualifiedName }})] = {{ printf "%q" .ResultHintTemplate }}
            {{- end }}
            {{- end }}
            if len(callRaw) > 0 {
                compiled, err := hints.CompileHintTemplates(callRaw, nil)
                if err != nil {
                    return err
                }
                reg.CallHints = compiled
            }
            if len(resultRaw) > 0 {
                compiled, err := hints.CompileHintTemplates(resultRaw, nil)
                if err != nil {
                    return err
                }
                reg.ResultHints = compiled
            }
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
    {{- if and (not (isMCPBacked .)) (eq .AgentToolsImportPath "") }}
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
