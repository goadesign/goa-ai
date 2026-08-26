{{- define "activityOptionsLiteral" -}}
{{ .EngineAlias }}.ActivityOptions{
{{- if ne .Queue "" }}
    Queue: {{ printf "%q" .Queue }},
{{- end }}
{{- if gt .ScheduleToStartTimeout 0 }}
    ScheduleToStartTimeout: {{ .TimeAlias }}.Duration({{ printf "%d" .ScheduleToStartTimeout }}),
{{- end }}
{{- if gt .StartToCloseTimeout 0 }}
    StartToCloseTimeout: {{ .TimeAlias }}.Duration({{ printf "%d" .StartToCloseTimeout }}),
{{- end }}
{{- if gt .HeartbeatTimeout 0 }}
    HeartbeatTimeout: {{ .TimeAlias }}.Duration({{ printf "%d" .HeartbeatTimeout }}),
{{- end }}
{{- if or (gt .RetryPolicy.MaxAttempts 0) (gt .RetryPolicy.InitialInterval 0) (ne .RetryPolicy.BackoffCoefficient 0.0) }}
    RetryPolicy: {{ .EngineAlias }}.RetryPolicy{
{{- if gt .RetryPolicy.MaxAttempts 0 }}
        MaxAttempts: {{ .RetryPolicy.MaxAttempts }},
{{- end }}
{{- if gt .RetryPolicy.InitialInterval 0 }}
        InitialInterval: {{ .TimeAlias }}.Duration({{ printf "%d" .RetryPolicy.InitialInterval }}),
{{- end }}
{{- if ne .RetryPolicy.BackoffCoefficient 0.0 }}
        BackoffCoefficient: {{ printf "%g" .RetryPolicy.BackoffCoefficient }},
{{- end }}
    },
{{- end }}
}
{{- end }}

// {{ .PackageNames.Register }} registers the generated agent components with the local runtime.
// This helper registers only with the runtime in this process. It does not
// publish the agent to a registry service.
func {{ .PackageNames.Register }}(ctx {{ .ContextAlias }}.Context, rt *{{ .RuntimeAlias }}.Runtime, cfg {{ .ConfigType }}) error {
    if rt == nil {
        return {{ .ErrorsAlias }}.New("runtime is required")
    }
    {{ .AgentVar }}, err := {{ .PackageNames.Constructor }}(cfg)
    if err != nil {
        return err
    }
    if err := rt.RegisterAgent(ctx, {{ .RuntimeAlias }}.AgentRegistration{
        ID:      {{ printf "%q" .ID }},
        Planner: {{ .AgentVar }}.Planner,
        Workflow: {{ .EngineAlias }}.WorkflowDefinition{
            Name:      {{ printf "%q" .Runtime.Workflow.Name }},
            TaskQueue: {{ printf "%q" .Runtime.Workflow.Queue }},
            Handler:   rt.ExecuteWorkflow,
        },
{{- if .PlanActivity }}
        PlanActivityName: {{ printf "%q" .Runtime.PlanActivity.Name }},
        PlanActivityOptions: {{ template "activityOptionsLiteral" .PlanActivity }},
{{- end }}
{{- if .ResumeActivity }}
        ResumeActivityName: {{ printf "%q" .Runtime.ResumeActivity.Name }},
        ResumeActivityOptions: {{ template "activityOptionsLiteral" .ResumeActivity }},
{{- end }}
{{- if .ExecuteToolActivity }}
        ExecuteToolActivity: {{ printf "%q" .Runtime.ExecuteTool.Name }},
        ExecuteToolActivityOptions: {{ template "activityOptionsLiteral" .ExecuteToolActivity }},
{{- end }}
        {{- if .Tools }}
        Specs: {{ .ToolSpecsAlias }}.Specs(),
        ToolMetadataLookup: {{ .ToolSpecsAlias }}.MetadataByName,
        RequiredLabels: {{ .ToolSpecsAlias }}.RequiredLabels(),
        {{- else }}
        Specs: nil,
        {{- end }}
        Policy: {{ .RuntimeAlias }}.RunPolicy{
{{- if gt .RunPolicy.Caps.MaxToolCalls 0 }}
            MaxToolCalls: {{ .RunPolicy.Caps.MaxToolCalls }},
{{- end }}
{{- if gt .RunPolicy.Caps.MaxRecoveryTurns 0 }}
            MaxRecoveryTurns: {{ .RunPolicy.Caps.MaxRecoveryTurns }},
{{- end }}
{{- if gt .RunPolicy.TimeBudget 0 }}
            TimeBudget: {{ .TimeAlias }}.Duration({{ printf "%d" .RunPolicy.TimeBudget }}),
{{- end }}
{{- if .RunPolicy.OnMissingFields }}
            {{- if eq .RunPolicy.OnMissingFields "finalize" }}
            OnMissingFields: {{ .RuntimeAlias }}.MissingFieldsFinalize,
            {{- else if eq .RunPolicy.OnMissingFields "await_clarification" }}
            OnMissingFields: {{ .RuntimeAlias }}.MissingFieldsAwaitClarification,
            {{- else if eq .RunPolicy.OnMissingFields "resume" }}
            OnMissingFields: {{ .RuntimeAlias }}.MissingFieldsResume,
            {{- end }}
{{- end }}
{{- if .RunPolicy.History }}
            History: func() {{ .RuntimeAlias }}.HistoryPolicy {
            {{- if eq .RunPolicy.History.Mode "keep_recent" }}
                return {{ .RuntimeAlias }}.KeepRecentTurns({{ .RunPolicy.History.KeepRecent }})
            {{- else if eq .RunPolicy.History.Mode "compress" }}
                historyCompression := {{ .RuntimeAlias }}.HistoryCompressionConfig{
                {{- if gt .RunPolicy.History.CompressAtTurns 0 }}
                    CompressAtTurns: {{ .RunPolicy.History.CompressAtTurns }},
                {{- end }}
                {{- if gt .RunPolicy.History.CompressAtMaxInputTokens 0 }}
                    CompressAtMaxInputTokens: {{ .RunPolicy.History.CompressAtMaxInputTokens }},
                {{- end }}
                {{- if gt .RunPolicy.History.KeepMaxTurns 0 }}
                    KeepMaxTurns: {{ .RunPolicy.History.KeepMaxTurns }},
                {{- end }}
                {{- if gt .RunPolicy.History.KeepMaxInputTokens 0 }}
                    KeepMaxInputTokens: {{ .RunPolicy.History.KeepMaxInputTokens }},
                {{- end }}
                }
                if cfg.HistoryCompression != nil {
                    historyCompression = *cfg.HistoryCompression
                }
                return {{ .RuntimeAlias }}.Compress(cfg.HistoryModel, historyCompression)
            {{- end }}
            }(),
{{- end }}
{{- if or .RunPolicy.Cache.AfterSystem .RunPolicy.Cache.AfterTools }}
            Cache: {{ .RuntimeAlias }}.CachePolicy{
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

    {{- if .MCPToolsets }}
    // Register MCP-backed toolsets using local executors and callers from config.
    if cfg.MCPCallers == nil {
        return {{ .FmtAlias }}.Errorf("mcp callers are required for agent %s", {{ printf "%q" .ID }})
    }
    {{- range .MCPToolsets }}
    {
        caller := cfg.MCPCallers[{{ .MCP.ConstName }}]
        if caller == nil {
            return {{ $.FmtAlias }}.Errorf("mcp caller for %s is required", {{ .MCP.ConstName }})
        }
        exec := {{ .AgentPackageHelperAlias }}.{{ .MCPExecutorConstructor }}(caller)
        // Register this remote toolset without exposing its generated service caller.
        reg := {{ $.RuntimeAlias }}.ToolsetRegistration{
            Name: {{ printf "%q" .QualifiedName }},
            // Decode calls and results with the schemas generated for this toolset.
            Specs: {{ .AgentPackageSpecsAlias }}.Specs(),
            ToolMetadataLookup: {{ .AgentPackageSpecsAlias }}.MetadataByName,
            Execute: func(ctx {{ $.ContextAlias }}.Context, call *{{ $.RuntimeAlias }}.ToolCall) (*{{ $.RuntimeAlias }}.ToolExecutionResult, error) {
                if call == nil {
                    return nil, {{ $.FmtAlias }}.Errorf("tool request is nil")
                }
                meta := {{ $.RuntimeAlias }}.ToolCallMetaFromCall(*call)
                result, err := exec.Execute(ctx, &meta, call)
                if err != nil {
                    return nil, err
                }
                if result == nil {
                    return nil, {{ $.FmtAlias }}.Errorf("executor returned nil execution result")
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

    // Application code registers toolsets that call Goa service methods.
    // Generated helpers register toolsets provided by another agent.
    return nil
}

{{- if .DirectToolsets }}
type {{ .PackageNames.UsedToolsetOptions }} struct {
    executors          map[string]{{ .RuntimeAlias }}.ToolCallExecutor
    resultMaterializers map[string]{{ .RuntimeAlias }}.ResultMaterializer
}

// {{ .PackageNames.RegisterUsedToolsets }} registers all non-MCP Used toolsets for this agent with
// the local runtime. Provide executors for each required toolset and optional
// result materializers through typed generated options.
//
// Example:
//   err := {{ .PackageNames.RegisterUsedToolsets }}(ctx, rt,
{{- range .DirectToolsets }}
//       {{ .ExecutorOption }}(exec),
{{- end }}
//   )
func {{ .PackageNames.RegisterUsedToolsets }}(ctx {{ .ContextAlias }}.Context, rt *{{ .RuntimeAlias }}.Runtime, opts ...func(*{{ .PackageNames.UsedToolsetOptions }})) error {
    if rt == nil {
        return {{ .ErrorsAlias }}.New("runtime is required")
    }
    cfg := &{{ .PackageNames.UsedToolsetOptions }}{
        executors:           make(map[string]{{ .RuntimeAlias }}.ToolCallExecutor),
        resultMaterializers: make(map[string]{{ .RuntimeAlias }}.ResultMaterializer),
    }
    for _, o := range opts {
        if o != nil {
            o(cfg)
        }
    }
    var missing []string
    {{- range .DirectToolsets }}
    if cfg.executors[{{ .RegistrationNameConst }}] == nil {
        missing = append(missing, {{ .RegistrationNameConst }})
    }
    {{- end }}
    if len(missing) > 0 {
        return {{ .FmtAlias }}.Errorf("missing executors for toolsets: %v", missing)
    }
    // Register non-MCP used toolsets that are not provided by agent-as-tool exports.
    {{- range .DirectToolsets }}
    {
        exec := cfg.executors[{{ .RegistrationNameConst }}]
        reg := {{ $.RuntimeAlias }}.ToolsetRegistration{
            Name:               {{ .RegistrationNameConst }},
            Specs:              {{ .AgentPackageSpecsAlias }}.Specs(),
            ToolMetadataLookup: {{ .AgentPackageSpecsAlias }}.MetadataByName,
            ResultMaterializer: cfg.resultMaterializers[{{ .RegistrationNameConst }}],
            Execute: func(ctx {{ $.ContextAlias }}.Context, call *{{ $.RuntimeAlias }}.ToolCall) (*{{ $.RuntimeAlias }}.ToolExecutionResult, error) {
                if call == nil {
                    return nil, {{ $.FmtAlias }}.Errorf("tool request is nil")
                }
                meta := {{ $.RuntimeAlias }}.ToolCallMetaFromCall(*call)
                result, err := exec.Execute(ctx, &meta, call)
                if err != nil {
                    return nil, err
                }
                if result == nil {
                    return nil, {{ $.FmtAlias }}.Errorf("executor returned nil execution result")
                }
                return result, nil
            },
        }
        {{- $hasCallHints := false -}}
        {{- $hasResultHints := false -}}
        {{- range .Tools }}
        {{- if .CallHintTemplate }}{{- $hasCallHints = true -}}{{- end }}
        {{- if .ResultHintTemplate }}{{- $hasResultHints = true -}}{{- end }}
        {{- end }}
        {{- if or $hasCallHints $hasResultHints }}
        if err := {{ .GeneratedHintsInstaller }}(&reg); err != nil {
            return err
        }
        {{- end }}
        if err := rt.RegisterToolset(reg); err != nil {
            return err
        }
    }
    {{- end }}
    return nil
}

    {{- range .DirectToolsets }}
// {{ .RegistrationNameConst }} is the local registration name for {{ .QualifiedName }}.
const {{ .RegistrationNameConst }} = {{ printf "%q" .QualifiedName }}

// {{ .ExecutorOption }} associates an executor for {{ .QualifiedName }}.
func {{ .ExecutorOption }}(exec {{ $.RuntimeAlias }}.ToolCallExecutor) func(*{{ $.PackageNames.UsedToolsetOptions }}) {
    return func(cfg *{{ $.PackageNames.UsedToolsetOptions }}) {
        cfg.executors[{{ .RegistrationNameConst }}] = exec
    }
}

// {{ .ResultMaterializerOption }} associates a result materializer for {{ .QualifiedName }}.
func {{ .ResultMaterializerOption }}(materializer {{ $.RuntimeAlias }}.ResultMaterializer) func(*{{ $.PackageNames.UsedToolsetOptions }}) {
    return func(cfg *{{ $.PackageNames.UsedToolsetOptions }}) {
        cfg.resultMaterializers[{{ .RegistrationNameConst }}] = materializer
    }
}

{{- $hasCallHints := false -}}
{{- $hasResultHints := false -}}
{{- range .Tools }}
{{- if .CallHintTemplate }}{{- $hasCallHints = true -}}{{- end }}
{{- if .ResultHintTemplate }}{{- $hasResultHints = true -}}{{- end }}
{{- end }}
{{- if or $hasCallHints $hasResultHints }}
func {{ .GeneratedHintsInstaller }}(reg *{{ $.RuntimeAlias }}.ToolsetRegistration) error {
    {{- if $hasCallHints }}
    callHints, err := {{ $.HintsAlias }}.CompileHintTemplates(map[{{ $.ToolsAlias }}.Ident]string{
    {{- range .Tools }}
    {{- if .CallHintTemplate }}
        {{ $.ToolsAlias }}.Ident({{ printf "%q" .QualifiedName }}): {{ printf "%q" .CallHintTemplate }},
    {{- end }}
    {{- end }}
    }, nil)
    if err != nil {
        return err
    }
    reg.CallHints = callHints
    {{- end }}
    {{- if $hasResultHints }}
    resultHints, err := {{ $.HintsAlias }}.CompileHintTemplates(map[{{ $.ToolsAlias }}.Ident]string{
    {{- range .Tools }}
    {{- if .ResultHintTemplate }}
        {{ $.ToolsAlias }}.Ident({{ printf "%q" .QualifiedName }}): {{ printf "%q" .ResultHintTemplate }},
    {{- end }}
    {{- end }}
    }, nil)
    if err != nil {
        return err
    }
    reg.ResultHints = resultHints
    {{- end }}
    return nil
}
{{- end }}
{{- end }}
{{- end }}
