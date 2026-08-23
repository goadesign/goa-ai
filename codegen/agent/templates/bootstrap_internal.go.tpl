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
        // Register deterministic executors so the generated example runs one
        // complete typed tool round trip before application code is added.
        if err := {{ .Alias }}.RegisterUsedToolsets(ctx, rt,
            {{- range .ExampleToolsets }}
            {{- $example := . }}
            {{ $a.Alias }}.With{{ goify .Toolset.PathName true }}Executor(agentsruntime.ToolCallExecutorFunc(
                func(ctx context.Context, _ *agentsruntime.ToolCallMeta, call *agentsruntime.ToolCall) (*agentsruntime.ToolExecutionResult, error) {
                    if call == nil {
                        return nil, fmt.Errorf("tool request is nil")
                    }
                    switch call.Name {
                    {{- range .Tools }}
                    case {{ $example.Alias }}.{{ .ConstName }}:
                        if _, err := {{ $example.Alias }}.Spec{{ .ConstName }}.Payload.Codec.FromJSON(call.Payload); err != nil {
                            return nil, fmt.Errorf("decode {{ .Name }} example payload: %w", err)
                        }
                        {{- if .Result }}
                        result, err := {{ $example.Alias }}.Spec{{ .ConstName }}.Result.Codec.FromJSON(
                            {{ $example.Alias }}.Spec{{ .ConstName }}.Result.ExampleJSON,
                        )
                        if err != nil {
                            return nil, fmt.Errorf("decode {{ .Name }} example result: %w", err)
                        }
                        return agentsruntime.Executed(&planner.ToolResult{Name: call.Name, Result: result}), nil
                        {{- else }}
                        return agentsruntime.Executed(&planner.ToolResult{Name: call.Name}), nil
                        {{- end }}
                    {{- end }}
                    default:
                        return nil, fmt.Errorf("unknown example tool %q", call.Name)
                    }
                },
            )),
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
