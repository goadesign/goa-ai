// Define flags for MCP endpoints (if any). Pass values via your cmd main.
{{- $hasMCP := false }}
{{- range .Agents }}{{- if .Agent.MCPToolsets }}{{ $hasMCP = true }}{{ end }}{{- end }}
{{- if $hasMCP }}
var (
    {{- range .Agents }}
        {{- range .Agent.MCPToolsets }}
    mcp{{ goify .ServiceName true }}{{ goify .SuiteName true }}Endpoint = flag.String("mcp-{{ ToLower .ServiceName }}-{{ ToLower .SuiteName }}-endpoint", "", "MCP {{ .QualifiedName }} HTTP endpoint (e.g., http://127.0.0.1:8080/rpc)")
        {{- end }}
    {{- end }}
)
{{- end }}

// New constructs a minimal runtime and registers all agents for this service.
// Replace options (engine, stores, telemetry) as you adopt production wiring.
func New(ctx context.Context) (*agentsruntime.Runtime, func(), error) {
    rt := agentsruntime.New()
    cleanup := func() {}

    // Register agents with example planners. Replace with your own planner impls.
    {{- range .Agents }}
    {{- $a := . }}
    {
        cfg := {{ .Alias }}.{{ .Agent.ConfigType }}{ Planner: {{ .PlannerAlias }}.New() }
        {{- if .Agent.MCPToolsets }}
        // Configure MCP callers for external toolsets.
        cfg.MCPCallers = map[string]mcpruntime.Caller{}
        {{- range .Agent.MCPToolsets }}
        if mcp{{ goify .ServiceName true }}{{ goify .SuiteName true }}Endpoint != nil && *mcp{{ goify .ServiceName true }}{{ goify .SuiteName true }}Endpoint != "" {
            caller, err := mcpruntime.NewHTTPCaller(ctx, mcpruntime.HTTPOptions{Endpoint: *mcp{{ goify .ServiceName true }}{{ goify .SuiteName true }}Endpoint})
            if err != nil { return nil, nil, err }
            cfg.MCPCallers[{{ $a.Alias }}.{{ .ConstName }}] = caller
        } else {
            cfg.MCPCallers[{{ $a.Alias }}.{{ .ConstName }}] = mcpruntime.CallerFunc(func(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
                return mcpruntime.CallResponse{}, fmt.Errorf("configure MCP caller for %s via -mcp-{{ ToLower .ServiceName }}-{{ ToLower .SuiteName }}-endpoint flag", {{ printf "%q" .QualifiedName }})
            })
        }
        {{- end }}
        {{- end }}
        if err := {{ .Alias }}.Register{{ .Agent.StructName }}(ctx, rt, cfg); err != nil {
            return nil, nil, err
        }
        {{- if .ExampleToolsets }}
        // Register the application-owned example executors.
        if err := {{ .Alias }}.RegisterUsedToolsets(ctx, rt,
            {{- range .ExampleToolsets }}
            {{ $a.Alias }}.With{{ goify .Toolset.PathName true }}Executor(
                agentsruntime.ToolCallExecutorFunc({{ .ExecutorAlias }}.Execute),
            ),
            {{- end }}
        ); err != nil {
            return nil, nil, err
        }
        {{- end }}
        {{- range .Toolsets }}
        // Register method-backed toolsets with default executors.
        if err := {{ .Alias }}.Register(ctx, rt); err != nil {
            return nil, nil, err
        }
        {{- end }}
    }
    {{- end }}

    return rt, cleanup, nil
}
