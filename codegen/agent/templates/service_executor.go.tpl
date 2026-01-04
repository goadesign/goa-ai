// Default service executor for {{ .Toolset.Name }}
// This factory builds a runtime.ToolCallExecutor that dispatches tool calls to
// user-provided per-tool callers. It decodes tool payloads with generated codecs,
// allows optional payload/result mappers, and returns results as-is (or mapped).
//
// The executor automatically wires the provided service client to the tool callers.
// You can override individual callers using the generated With<Tool> options.
//
// Example:
//
//   client := atlasdata.NewClient(...)
//   exec := {{ .Toolset.PackageName }}.New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec(client)
//
//   // Register:
//   // reg := {{ .Agent.GoName }}{{ goify .Toolset.PathName true }}.New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}ToolsetRegistration(exec)
//   // rt.RegisterToolset(reg)

type (
    seCfg struct {
        callers    map[tools.Ident]func(context.Context, any) (any, error)
        mapPayload func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)
        mapResult  func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)
        injectors  []ToolInterceptor
    }
    // ExecOpt customizes the default service executor.
    ExecOpt interface{ apply(*seCfg) }

    // ToolInterceptor hooks into tool execution to inject context or modify payloads.
    ToolInterceptor interface {
        // Inject mutates the service method payload before the client call.
        // It receives the fully mapped service payload (e.g. *GetAlarmsPayload)
        // and the tool call metadata.
        Inject(ctx context.Context, payload any, meta *runtime.ToolCallMeta) error
    }
    
    ToolInterceptorFunc func(context.Context, any, *runtime.ToolCallMeta) error
)

func (f ToolInterceptorFunc) Inject(ctx context.Context, p any, m *runtime.ToolCallMeta) error {
    return f(ctx, p, m)
}

type execOptFunc func(*seCfg)

func (f execOptFunc) apply(c *seCfg) { f(c) }

// WithPayloadMapper installs a mapper for tool payload -> method payload.
func WithPayloadMapper(f func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) { c.mapPayload = f })
}

// WithResultMapper installs a mapper for method result -> tool result.
func WithResultMapper(f func(tools.Ident, any, *runtime.ToolCallMeta) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) { c.mapResult = f })
}

// WithInterceptors adds interceptors to the executor.
func WithInterceptors(interceptors ...ToolInterceptor) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        c.injectors = append(c.injectors, interceptors...)
    })
}

// WithClient wires default callers for all method-backed tools using the
// provided service client. This is a convenience for direct service wiring;
// adapter-style executors can instead provide callers via the With<Tool>
// options without supplying a client.
func WithClient(client *{{ .ServicePkgAlias }}.Client) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        if client == nil {
            return
        }
        if c.callers == nil {
            c.callers = make(map[tools.Ident]func(context.Context, any) (any, error))
        }
        {{- range .Toolset.Tools }}
        {{- if .IsMethodBacked }}
        c.callers[tools.Ident({{ printf "%q" .QualifiedName }})] = func(ctx context.Context, args any) (any, error) {
            return client.{{ .MethodGoName }}(ctx, args.({{ .MethodPayloadTypeRef }}))
        }
        {{- end }}
        {{- end }}
    })
}

{{- range .Toolset.Tools }}
{{- if .IsMethodBacked }}
// With{{ goify .Name true }} sets the caller for {{ .QualifiedName }}.
func With{{ goify .Name true }}(f func(context.Context, any) (any, error)) ExecOpt {
    return execOptFunc(func(c *seCfg) {
        if c.callers == nil {
            c.callers = make(map[tools.Ident]func(context.Context, any) (any, error))
        }
        c.callers[tools.Ident({{ printf "%q" .QualifiedName }})] = f
    })
}
{{- end }}
{{- end }}

// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec returns a ToolCallExecutor that
// decodes tool payloads with generated codecs, applies optional mappers, calls user-provided
// per-tool callers (wired from the client via WithClient), and maps results back.
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}Exec(opts ...ExecOpt) runtime.ToolCallExecutor {
    var cfg seCfg
    cfg.callers = make(map[tools.Ident]func(context.Context, any) (any, error))

    for _, o := range opts {
        if o != nil {
            o.apply(&cfg)
        }
    }
    // Preflight: ensure callers are provided for all method-backed tools.
    {
        var missing []string
        {{- range .Toolset.Tools }}
        {{- if .IsMethodBacked }}
        if cfg.callers == nil || cfg.callers[tools.Ident({{ printf "%q" .QualifiedName }})] == nil {
            // report the fully-qualified tool for clarity
            missing = append(missing, {{ printf "%q" .QualifiedName }})
        }
        {{- end }}
        {{- end }}
        if len(missing) > 0 {
            panic(fmt.Errorf("service executor missing callers for tools: %s", strings.Join(missing, ", ")))
        }
    }
    return runtime.ToolCallExecutorFunc(func(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error) {
        if call == nil {
            return &planner.ToolResult{Error: planner.NewToolError("tool request is nil")}, nil
        }
        if meta == nil {
            return &planner.ToolResult{Error: planner.NewToolError("tool call meta is nil")}, nil
        }
        // Lookup caller registered for this tool.
        caller := cfg.callers[call.Name]
        if caller == nil {
            return &planner.ToolResult{
                Name: call.Name,
                Error: planner.NewToolError(
                    fmt.Sprintf(
                        "no service caller registered for tool %q in toolset %q; "+
                            "ensure the appropriate With... option is wired when constructing the executor",
                        call.Name,
                        "{{ .Toolset.QualifiedName }}",
                    ),
                ),
            }, nil
        }
        // Decode tool payload from canonical JSON into a typed struct using the
        // generated payload codec. Method‑backed tools always have a payload
        // codec; missing codecs are treated as programmer errors.
        var toolArgs any
        if len(call.Payload) > 0 {
            pc, ok := {{ $.Toolset.SpecsPackageName }}.PayloadCodec(string(call.Name))
            if !ok || pc == nil || pc.FromJSON == nil {
                panic(fmt.Errorf("missing payload codec for tool %q in toolset %q", call.Name, "{{ .Toolset.QualifiedName }}"))
            }
            val, err := pc.FromJSON(call.Payload)
            if err != nil {
                return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
            }
            toolArgs = val
        }
        // Map to method payload
        var methodIn any
        if cfg.mapPayload != nil {
            var err error
            methodIn, err = cfg.mapPayload(call.Name, toolArgs, meta)
            if err != nil {
                return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
            }
        } else {
             // Default mapping using generated transforms
             switch call.Name {
             {{- range .Toolset.Tools }}
             {{- if .IsMethodBacked }}
             case tools.Ident({{ printf "%q" .QualifiedName }}):
                 {{- if .PayloadAliasesMethod }}
                 methodIn = toolArgs
                 {{- if .InjectedFields }}
                 p := methodIn.({{ .MethodPayloadTypeRef }})
                 {{- range .InjectedFields }}
                 p.{{ goify . true }} = meta.{{ goify . true }}
                 {{- end }}
                 methodIn = p
                 {{- end }}
                 {{- else }}
                 // Call generated transform
                 p := {{ $.Toolset.SpecsPackageName }}.Init{{ goify .Name true }}MethodPayload(toolArgs.(*{{ $.Toolset.SpecsPackageName }}.{{ .ConstName }}Payload))
                 {{- if .InjectedFields }}
                 {{- range .InjectedFields }}
                 p.{{ goify . true }} = meta.{{ goify . true }}
                 {{- end }}
                 {{- end }}
                 methodIn = p
                 {{- end }}
             {{- end }}
             {{- end }}
             default:
                 methodIn = toolArgs
             }
        }
        
        // Apply interceptors (injection)
        for _, inj := range cfg.injectors {
            if err := inj.Inject(ctx, methodIn, meta); err != nil {
                 return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err)}, nil
            }
        }

        // Invoke caller
        methodOut, err := caller(ctx, methodIn)
        if err != nil {
            tr := &planner.ToolResult{
                Name:  call.Name,
                Error: planner.ToolErrorFromError(err),
            }
            // Attach structured retry hints when the error provides them.
            var provider planner.RetryHintProvider
            if errors.As(err, &provider) {
                if hint := provider.RetryHint(call.Name); hint != nil {
                    tr.RetryHint = hint
                }
            }
            return tr, nil
        }
        // Map back to tool result
        var result any
        if cfg.mapResult != nil {
            var e error
            result, e = cfg.mapResult(call.Name, methodOut, meta)
            if e != nil {
                return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(e)}, nil
            }
        } else {
            // Default mapping using generated transforms
            switch call.Name {
            {{- range .Toolset.Tools }}
            {{- if .IsMethodBacked }}
            case tools.Ident({{ printf "%q" .QualifiedName }}):
                {{- if .ResultAliasesMethod }}
                result = methodOut
                {{- else }}
                result = {{ $.Toolset.SpecsPackageName }}.Init{{ goify .Name true }}ToolResult(methodOut.({{ .MethodResultTypeRef }}))
                {{- end }}
            {{- end }}
            {{- end }}
            default:
                result = methodOut
            }
        }

        // Build final tool result. For tools that declare a typed artifact
        // sidecar, we derive it from the concrete service method result via
        // generated transforms and attach it as a non-model artifact.
        {{- $hasSidecar := false }}
        {{- range .Toolset.Tools }}
            {{- if and .IsMethodBacked .Artifact }}
                {{- $hasSidecar = true }}
            {{- end }}
        {{- end }}
        {{- if $hasSidecar }}
        var artifacts []*planner.Artifact
        switch call.Name {
        {{- range .Toolset.Tools }}
        {{- if and .IsMethodBacked .Artifact }}
        case tools.Ident({{ printf "%q" .QualifiedName }}):
            if toolArtifactsEnabled(call.ArtifactsMode, {{ if .ArtifactsDefaultOn }}true{{ else }}false{{ end }}) {
                if mr, ok := methodOut.({{ .MethodResultTypeRef }}); ok {
                    if sc := {{ $.Toolset.SpecsPackageName }}.Init{{ goify .Name true }}SidecarFromMethodResult(mr); sc != nil {
                        artifacts = []*planner.Artifact{
                            {
                                Kind:       {{ printf "%q" .ArtifactKind }},
                                Data:       sc,
                                SourceTool: call.Name,
                            },
                        }
                    }
                }
            }
        {{- end }}
        {{- end }}
        }
        return &planner.ToolResult{
            Name:      call.Name,
            Result:    result,
            Artifacts: artifacts,
        }, nil
        {{- else }}
        return &planner.ToolResult{
            Name:   call.Name,
            Result: result,
        }, nil
        {{- end }}
    })
}

{{- if $hasSidecar }}

func toolArtifactsEnabled(mode tools.ArtifactsMode, defaultOn bool) bool {
	switch mode {
	case tools.ArtifactsModeOn:
		return true
	case tools.ArtifactsModeOff:
		return false
	case tools.ArtifactsModeAuto, "":
		return defaultOn
	default:
		return defaultOn
	}
}

{{- end }}
