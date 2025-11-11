// bootstrapAgents creates the agent runtime. By default it uses in-memory stores only
// (no workflow engine) so examples run without external dependencies. To enable Temporal,
// pass CLI flags (consistent with Goa services) which default from environment variables.
//
// Flags:
// -agents-use-temporal (env AGENTS_USE_TEMPORAL)
// -temporal-hostport   (env TEMPORAL_HOSTPORT, default 127.0.0.1:7233)
// -temporal-namespace  (env TEMPORAL_NAMESPACE, default "default")
// -agents-task-queue   (env AGENTS_TASK_QUEUE, default "{{ .Service.PathName }}.agents")
var (
    flagUseTemporal     = flag.Bool("agents-use-temporal", envBool("AGENTS_USE_TEMPORAL", false), "Enable Temporal engine for agents")
    flagTemporalHost    = flag.String("temporal-hostport", envString("TEMPORAL_HOSTPORT", "127.0.0.1:7233"), "Temporal host:port")
    flagTemporalNS      = flag.String("temporal-namespace", envString("TEMPORAL_NAMESPACE", "default"), "Temporal namespace")
    flagAgentsTaskQueue = flag.String("agents-task-queue", envString("AGENTS_TASK_QUEUE", "{{ .Service.PathName }}.agents"), "Agents worker task queue")
)

func envString(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func envBool(key string, def bool) bool {
    v := os.Getenv(key)
    if v == "" {
        return def
    }
    switch strings.ToLower(v) {
    case "1", "true", "yes", "y", "on":
        return true
    case "0", "false", "no", "n", "off":
        return false
    default:
        return def
    }
}
func bootstrapAgents(ctx context.Context) (*agentsruntime.Runtime, func(), error) {
    useTemporal := *flagUseTemporal
    if useTemporal {
        host := *flagTemporalHost
        ns := *flagTemporalNS
        tq := *flagAgentsTaskQueue
        eng, err := runtimeTemporal.New(runtimeTemporal.Options{
            ClientOptions: &temporalclient.Options{ HostPort: host, Namespace: ns },
            WorkerOptions: runtimeTemporal.WorkerOptions{ TaskQueue: tq },
        })
        if err != nil {
            return nil, nil, err
        }
        rt := agentsruntime.New(
            agentsruntime.WithEngine(eng),
            agentsruntime.WithMemoryStore(meminmem.New()),
            agentsruntime.WithRunStore(runinmem.New()),
        )
        // Register all agents in this service.
        {{- range .Agents }}
        {{- $agent := . }}
        cfg{{ $agent.GoName }} := {{ $agent.PackageName }}.{{ $agent.ConfigType }}{ Planner: new{{ .GoName }}Planner() }
        {{- if .MCPToolsets }}
        callers{{ $agent.GoName }}, err := configure{{ .GoName }}MCPCallers(ctx)
        if err != nil { _ = eng.Close(); return nil, nil, err }
        cfg{{ $agent.GoName }}.MCPCallers = callers{{ $agent.GoName }}
        {{- end }}
        if err := {{ $agent.PackageName }}.Register{{ $agent.StructName }}(ctx, rt, cfg{{ $agent.GoName }}); err != nil {
            _ = eng.Close()
            return nil, nil, err
        }
        {{- end }}
        return rt, func() { _ = eng.Close() }, nil
    }
    // In-memory runtime for quick starts and CI (no engine).
    rt := agentsruntime.New(
        agentsruntime.WithMemoryStore(meminmem.New()),
        agentsruntime.WithRunStore(runinmem.New()),
    )
    cleanup := func() {}
    return rt, cleanup, nil
}

{{- range .Agents }}
{{- if .MCPToolsets }}
{{- $agent := . }}
// configure{{ $agent.GoName }}MCPCallers returns the MCP callers required by the {{ $agent.Name }} agent.
// Configure via flags/env:
//   -mcp-<suite>-mode: http|sse|stdio|stub (env MCP_<SUITE>_MODE)
//   -mcp-<suite>-endpoint: http(s) endpoint for HTTP/SSE (env MCP_<SUITE>_ENDPOINT)
//   -mcp-<suite>-cmd: stdio command (env MCP_<SUITE>_CMD)
//   -mcp-<suite>-args: stdio args comma-separated (env MCP_<SUITE>_ARGS)
func configure{{ $agent.GoName }}MCPCallers(ctx context.Context) (map[string]mcpruntime.Caller, error) {
    callers := make(map[string]mcpruntime.Caller, {{ len $agent.MCPToolsets }})
    {{- range $agent.MCPToolsets }}
    {
        // Generic flags/env applied to all MCP suites in the example for simplicity
        // Env vars: MCP_MODE, MCP_ENDPOINT, MCP_CMD, MCP_ARGS
        mode := strings.ToLower(envString("MCP_MODE", "stub"))
        endpoint := envString("MCP_ENDPOINT", "http://127.0.0.1:8080/rpc")
        cmd := envString("MCP_CMD", "")
        argstr := envString("MCP_ARGS", "")
        var caller mcpruntime.Caller
        var err error
        switch mode {
        case "http":
            caller, err = mcpruntime.NewHTTPCaller(ctx, mcpruntime.HTTPOptions{Endpoint: endpoint})
        case "sse":
            caller, err = mcpruntime.NewSSECaller(ctx, mcpruntime.HTTPOptions{Endpoint: endpoint})
        case "stdio":
            var args []string
            if argstr != "" {
                args = strings.Split(argstr, ",")
            }
            caller, err = mcpruntime.NewStdioCaller(ctx, mcpruntime.StdioOptions{Command: cmd, Args: args})
        default:
            caller = mcpruntime.CallerFunc(func(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
                return mcpruntime.CallResponse{}, fmt.Errorf("configure MCP caller for %s (mode=%s)", {{ printf "%q" .QualifiedName }}, mode)
            })
        }
        if err != nil {
            return nil, err
        }
        callers[{{ $agent.PackageName }}.{{ .ConstName }}] = caller
    }
    {{- end }}
    return callers, nil
}
{{- end }}
{{- end }}
