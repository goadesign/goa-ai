// Define flags for MCP endpoints (if any). Pass values via your cmd main.
{{- if .HasMCP }}
var (
    {{- range .Toolsets }}
        {{- if .MCP }}
    {{ .MCP.EndpointVar }} = {{ $.FlagAlias }}.String({{ printf "%q" .MCP.FlagName }}, "", "MCP {{ .Toolset.QualifiedName }} HTTP endpoint (e.g., http://127.0.0.1:8080/rpc)")
        {{- end }}
    {{- end }}
)
{{- end }}

// New constructs a runtime with the host-owned store and registers all agents
// for this service. Replace the example planner and tool executors as you adopt
// production wiring.
func New(ctx {{ .ContextAlias }}.Context, store {{ .StorageAlias }}.Store) (*{{ .AgentRuntimeAlias }}.Runtime, func(), error) {
    rt := {{ .AgentRuntimeAlias }}.New(store)
    cleanup := func() {}

    // Register each executable once. Agent definitions may share these bindings.
    {{- range .Toolsets }}
    {
        {{- if .MCP }}
        caller, err := {{ $.MCPRuntimeAlias }}.NewHTTPCaller({{ $.MCPRuntimeAlias }}.HTTPOptions{
            Endpoint: *{{ .MCP.EndpointVar }},
            ClientInfo: {{ $.MCPRuntimeAlias }}.ClientInfo{Name: {{ printf "%q" $.Service.Service.Name }}, Version: {{ printf "%q" $.ClientVersion }}},
        })
        if err != nil { return nil, nil, err }
        exec := {{ .ExecutorAlias }}.{{ .Toolset.MCPExecutorConstructor }}(caller)
        {{- else }}
        exec := {{ $.AgentRuntimeAlias }}.ToolCallExecutorFunc({{ .ExecutorAlias }}.Execute)
        {{- end }}
        registration := {{ $.AgentRuntimeAlias }}.ToolsetRegistration{
            Name: {{ printf "%q" .Toolset.QualifiedName }},
{{- if .MCP }}
            ActivityRetryPolicy: &{{ $.EngineAlias }}.RetryPolicy{MaxAttempts: 1},
{{- end }}
            Specs: {{ .SpecsAlias }}.Specs(),
            ToolMetadataLookup: {{ .SpecsAlias }}.MetadataByName,
            Execute: func(ctx {{ $.ContextAlias }}.Context, call *{{ $.AgentRuntimeAlias }}.ToolCall) (*{{ $.AgentRuntimeAlias }}.ToolExecutionResult, error) {
                meta := {{ $.AgentRuntimeAlias }}.ToolCallMetaFromCall(*call)
                return exec.Execute(ctx, &meta, call)
            },
        }
        {{- $hasCallHints := false -}}
        {{- $hasResultHints := false -}}
        {{- range .Toolset.Tools }}
        {{- if .CallHintTemplate }}{{- $hasCallHints = true -}}{{- end }}
        {{- if .ResultHintTemplate }}{{- $hasResultHints = true -}}{{- end }}
        {{- end }}
        {{- if $hasCallHints }}
        callHints, err := {{ $.HintsAlias }}.CompileHintTemplates(map[{{ $.ToolsAlias }}.Ident]string{
            {{- range .Toolset.Tools }}
            {{- if .CallHintTemplate }}
            {{ printf "%q" .QualifiedName }}: {{ printf "%q" .CallHintTemplate }},
            {{- end }}
            {{- end }}
        }, nil)
        if err != nil { return nil, nil, err }
        registration.CallHints = callHints
        {{- end }}
        {{- if $hasResultHints }}
        resultHints, err := {{ $.HintsAlias }}.CompileHintTemplates(map[{{ $.ToolsAlias }}.Ident]string{
            {{- range .Toolset.Tools }}
            {{- if .ResultHintTemplate }}
            {{ printf "%q" .QualifiedName }}: {{ printf "%q" .ResultHintTemplate }},
            {{- end }}
            {{- end }}
        }, nil)
        if err != nil { return nil, nil, err }
        registration.ResultHints = resultHints
        {{- end }}
        if err := rt.RegisterToolset(registration); err != nil { return nil, nil, err }
    }
    {{- end }}

    // Register agents with example planners. Replace with your own planner implementations.
    {{- range .Agents }}
    {
        cfg := {{ .Alias }}.{{ .Agent.ConfigType }}{Planner: {{ .PlannerAlias }}.New()}
        {{- if .HasRegistrySources }}
        // Connect each declared registry before the first run.
        {{- end }}
        if err := {{ .Alias }}.{{ .Agent.PackageNames.Register }}(ctx, rt, cfg); err != nil {
            return nil, nil, err
        }
    }
    {{- end }}

    return rt, cleanup, nil
}
