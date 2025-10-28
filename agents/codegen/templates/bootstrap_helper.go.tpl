// bootstrapAgents initializes the workflow engine + runtime and registers agents.
// The caller is responsible for closing the returned cleanup function.
func bootstrapAgents(ctx context.Context) (*agentsruntime.Runtime, func(), error) {
	eng, err := runtimeTemporal.New(runtimeTemporal.Options{
		ClientOptions: &temporalclient.Options{
			HostPort:  "127.0.0.1:7233",
			Namespace: "default",
		},
		WorkerOptions: runtimeTemporal.WorkerOptions{TaskQueue: "{{ .Service.PathName }}.agents"},
	})
	if err != nil {
		return nil, nil, err
	}
	rt := agentsruntime.New(agentsruntime.Options{
		Engine:      eng,
		MemoryStore: meminmem.New(),
		RunStore:    runinmem.New(),
	})

	// Register all agents in this service.
	{{- range .Agents }}
	{{- $agent := . }}
	cfg{{ $agent.GoName }} := {{ $agent.PackageName }}.{{ $agent.ConfigType }}{
		Planner: new{{ .GoName }}Planner(),
	}
	{{- if .MCPToolsets }}
	callers{{ $agent.GoName }}, err := configure{{ .GoName }}MCPCallers(ctx)
	if err != nil {
		_ = eng.Close()
		return nil, nil, err
	}
	cfg{{ $agent.GoName }}.MCPCallers = callers{{ $agent.GoName }}
	{{- end }}
	if err := {{ $agent.PackageName }}.Register{{ $agent.StructName }}(ctx, rt, cfg{{ $agent.GoName }}); err != nil {
		_ = eng.Close()
		return nil, nil, err
	}
	{{- end }}

	return rt, func() { _ = eng.Close() }, nil
}

{{- range .Agents }}
{{- if .MCPToolsets }}
{{- $agent := . }}
// configure{{ $agent.GoName }}MCPCallers returns the MCP callers required by the {{ $agent.Name }} agent.
// Replace the stubs with real callers (e.g., mcpruntime.NewHTTPCaller) before running agents.
func configure{{ $agent.GoName }}MCPCallers(ctx context.Context) (map[string]mcpruntime.Caller, error) {
	_ = ctx
	callers := make(map[string]mcpruntime.Caller, {{ len $agent.MCPToolsets }})
	{{- range $agent.MCPToolsets }}
	callers[{{ $agent.PackageName }}.{{ .ConstName }}] = mcpruntime.CallerFunc(func(ctx context.Context, req mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
		return mcpruntime.CallResponse{}, fmt.Errorf("configure MCP caller for %s in cmd/{{ $.Service.PathName }}/agents_bootstrap.go", {{ printf "%q" .QualifiedName }})
	})
	{{- end }}
	return callers, nil
}
{{- end }}
{{- end }}
