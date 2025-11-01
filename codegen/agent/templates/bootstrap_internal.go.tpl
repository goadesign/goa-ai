// Package bootstrap wires the goa-ai runtime and registers generated agents.
// This scaffold is application-owned: edit and maintain it; it is not re-generated
// by `goa gen`. Use it from your cmd main or workers to initialize agents.

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
    rt := agentsruntime.New(agentsruntime.Options{})
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
