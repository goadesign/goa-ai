// Define flags for MCP endpoints (if any). Pass values via your cmd main.
{{- if .HasMCP }}
var (
    {{- range .Agents }}
        {{- range .MCPToolsets }}
    {{ .EndpointVar }} = {{ $.FlagAlias }}.String({{ printf "%q" .FlagName }}, "", "MCP {{ .QualifiedName }} HTTP endpoint (e.g., http://127.0.0.1:8080/rpc)")
        {{- end }}
    {{- end }}
)
{{- end }}

// New constructs a minimal runtime and registers all agents for this service.
// Replace options (engine, stores, telemetry) as you adopt production wiring.
func New(ctx {{ .ContextAlias }}.Context) (*{{ .AgentRuntimeAlias }}.Runtime, func(), error) {
    rt := {{ .AgentRuntimeAlias }}.New()
    cleanup := func() {}

    // Register agents with example planners. Replace with your own planner impls.
    {{- range .Agents }}
    {{- $a := . }}
    {
        cfg := {{ .Alias }}.{{ .Agent.ConfigType }}{ Planner: {{ .PlannerAlias }}.New() }
        {{- if .MCPToolsets }}
        // Configure MCP callers for external toolsets.
        cfg.MCPCallers = map[string]{{ $.MCPRuntimeAlias }}.Caller{}
        {{- range .MCPToolsets }}
        if {{ .EndpointVar }} != nil && *{{ .EndpointVar }} != "" {
            caller, err := {{ $.MCPRuntimeAlias }}.NewHTTPCaller(ctx, {{ $.MCPRuntimeAlias }}.HTTPOptions{
                Endpoint: *{{ .EndpointVar }},
                ClientInfo: {{ $.MCPRuntimeAlias }}.ClientInfo{Name: {{ printf "%q" $.Service.Service.Name }}, Version: {{ printf "%q" $.ClientVersion }}},
            })
            if err != nil { return nil, nil, err }
            cfg.MCPCallers[{{ $a.Alias }}.{{ .ConstName }}] = caller
        } else {
            cfg.MCPCallers[{{ $a.Alias }}.{{ .ConstName }}] = {{ $.MCPRuntimeAlias }}.CallerFunc(func(ctx {{ $.ContextAlias }}.Context, req {{ $.MCPRuntimeAlias }}.CallRequest) ({{ $.MCPRuntimeAlias }}.CallResponse, error) {
                return {{ $.MCPRuntimeAlias }}.CallResponse{}, {{ $.FmtAlias }}.Errorf("configure MCP caller for %s via -{{ .FlagName }} flag", {{ printf "%q" .QualifiedName }})
            })
        }
        {{- end }}
        {{- end }}
        if err := {{ .Alias }}.{{ .Agent.PackageNames.Register }}(ctx, rt, cfg); err != nil {
            return nil, nil, err
        }
        {{- if .ExampleToolsets }}
        // Register the application-owned example executors.
        if err := {{ .Alias }}.{{ .Agent.PackageNames.RegisterUsedToolsets }}(ctx, rt,
            {{- range .ExampleToolsets }}
            {{ $a.Alias }}.{{ .Toolset.ExecutorOption }}(
                {{ $.AgentRuntimeAlias }}.ToolCallExecutorFunc({{ .ExecutorAlias }}.Execute),
            ),
            {{- end }}
        ); err != nil {
            return nil, nil, err
        }
        {{- end }}
    }
    {{- end }}

    return rt, cleanup, nil
}
